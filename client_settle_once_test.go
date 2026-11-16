package centrifuge

// Request settlement tests from upstream centrifugal/centrifuge-go #127 and
// #142: a request callback runs exactly once, and a call whose send fails
// during a teardown returns.

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/centrifugal/protocol"
)

// noopTransport is a transport whose Write always succeeds without actually
// sending anything anywhere, so sendAsync's ReadTimeout goroutine gets
// started without needing a real connection.
type noopTransport struct{}

func (noopTransport) Read() (*protocol.Reply, *disconnect, error)  { return nil, nil, io.EOF }
func (noopTransport) Write(*protocol.Command, time.Duration) error { return nil }
func (noopTransport) Close() error                                 { return nil }

// TestClient_RequestCallbackNotInvokedTwiceOnTimeoutRace delivers a reply while
// the request's ReadTimeout fires, and checks that the callback runs once. The
// callback outlasts callTimeoutGrace, so the timeout path looks the request up
// while the reply's callback is still running.
//
// Its waits are timed on purpose. The reply and the timeout race in two
// goroutines with no hook to order them, and the test repeats the race to hit
// both orders. The final wait checks that a second call never happens, which
// needs a window: it outlasts the timeout path's grace, after which that path
// has either taken the request or found it gone.
func TestClient_RequestCallbackNotInvokedTwiceOnTimeoutRace(t *testing.T) {
	config := Config{ReadTimeout: time.Microsecond}
	c := NewJsonClient("ws://localhost:9000/connection/websocket", config)
	defer c.Close()
	c.transport = noopTransport{}
	c.closeCh = make(chan struct{})

	const iterations = 20
	for i := 0; i < iterations; i++ {
		cmd := &protocol.Command{Id: uint32(i + 1)}
		var calls int32
		done := make(chan struct{}, 2)
		cb := func(reply *protocol.Reply, err error) {
			atomic.AddInt32(&calls, 1)
			time.Sleep(callTimeoutGrace + 20*time.Millisecond)
			done <- struct{}{}
		}

		if err := c.sendAsync(cmd, cb); err != nil {
			t.Fatalf("sendAsync: %v", err)
		}
		// Deliver the real reply concurrently with the sendAsync-spawned
		// ReadTimeout goroutine, which fires almost immediately given the
		// tiny ReadTimeout configured above.
		c.handle(&protocol.Reply{Id: cmd.Id})

		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatalf("request %d: callback was never invoked", cmd.Id)
		}
		// Give the loser of the race a chance to (incorrectly) also fire.
		time.Sleep(callTimeoutGrace + 30*time.Millisecond)
		if got := atomic.LoadInt32(&calls); got != 1 {
			t.Fatalf("request %d: callback invoked %d times, want 1", cmd.Id, got)
		}
	}
}

// failingWriteTransport fails every write after running beforeFail, a hook to
// interleave a teardown with the failing write.
type failingWriteTransport struct {
	beforeFail func()
}

func (t *failingWriteTransport) Read() (*protocol.Reply, *disconnect, error) {
	select {}
}

func (t *failingWriteTransport) Write(*protocol.Command, time.Duration) error {
	if t.beforeFail != nil {
		t.beforeFail()
	}
	return errors.New("write failed")
}

func (t *failingWriteTransport) Close() error {
	return nil
}

func TestCallReturnsWhenSendFailsDuringTeardown(t *testing.T) {
	client := NewProtobufClient("ws://127.0.0.1:1/connection/websocket", Config{})
	t.Cleanup(func() { client.Close() })
	ft := &failingWriteTransport{}
	ft.beforeFail = func() {
		// The connection is torn down while the command is written: the
		// teardown fails every pending request, this one included. It runs
		// the callbacks in goroutines, so wait until this request's callback
		// has finished: the write then fails with the request already settled,
		// which is the order that made Publish hang.
		settled := make(chan struct{})
		client.requestsMu.Lock()
		for id, req := range client.requests {
			cb := req.cb
			client.requests[id] = request{cb: func(r *protocol.Reply, err error) {
				cb(r, err)
				close(settled)
			}}
		}
		client.requestsMu.Unlock()

		client.mu.Lock()
		client.clearConnectedState()
		client.mu.Unlock()

		select {
		case <-settled:
		case <-time.After(time.Second):
			t.Error("the teardown never settled the pending request")
		}
	}
	client.mu.Lock()
	client.state = StateConnected
	client.transport = ft
	client.mu.Unlock()

	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, err := client.Publish(ctx, "ch", []byte(`{}`))
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error for a publish whose send failed")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Publish never returned after its send failed during a teardown, despite its 1s context")
	}
}

// hangTimeout only reports a hang. A passing run never waits for it.
const hangTimeout = 10 * time.Second

func waitFor[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(hangTimeout):
		t.Fatalf("%s did not happen within %v", what, hangTimeout)
		panic("unreachable")
	}
}

// blockingCallback counts the calls of a request callback. The first call
// reports its error on first and then blocks until release is closed, so that a
// test can make a second settle attempt while the first callback still runs.
type blockingCallback struct {
	mu      sync.Mutex
	calls   int
	first   chan error
	release chan struct{}
}

func newBlockingCallback() *blockingCallback {
	return &blockingCallback{first: make(chan error, 1), release: make(chan struct{})}
}

func (b *blockingCallback) cb(_ *protocol.Reply, err error) {
	b.mu.Lock()
	b.calls++
	n := b.calls
	b.mu.Unlock()
	if n == 1 {
		b.first <- err
		<-b.release
	}
}

func (b *blockingCallback) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

// newWritableClient returns a client whose writes succeed and go nowhere, so a
// test plays the server by calling handle.
func newWritableClient(t *testing.T, readTimeout time.Duration) (*Client, chan struct{}) {
	t.Helper()
	c := NewJsonClient("ws://127.0.0.1:1/connection/websocket", Config{ReadTimeout: readTimeout})
	t.Cleanup(c.Close)
	closeCh := make(chan struct{})
	c.mu.Lock()
	c.transport = noopTransport{}
	c.closeCh = closeCh
	c.mu.Unlock()
	return c, closeCh
}

func TestClient_sendAsyncRemovesRequestWhenSendFails(t *testing.T) {
	c := NewProtobufClient("ws://127.0.0.1:1/connection/websocket", Config{})
	t.Cleanup(c.Close)
	cmd := &protocol.Command{Id: c.nextCmdID()}
	// Holding the client lock keeps the teardown that the failed send starts
	// from running before the request is checked.
	c.mu.Lock()
	c.state = StateConnected
	c.transport = &failingWriteTransport{}
	err := c.sendAsync(cmd, func(*protocol.Reply, error) {})
	c.requestsMu.RLock()
	_, pending := c.requests[cmd.Id]
	c.requestsMu.RUnlock()
	c.mu.Unlock()
	if err == nil {
		t.Fatal("sendAsync returned nil for a failed send that nothing else settled")
	}
	if pending {
		t.Fatal("the request is still pending after sendAsync returned its error, so the teardown settles it a second time")
	}
}

func TestClient_sendAsyncDropsLateReplyAfterTimeout(t *testing.T) {
	c, _ := newWritableClient(t, time.Nanosecond)
	b := newBlockingCallback()
	defer close(b.release)
	cmd := &protocol.Command{Id: c.nextCmdID()}
	if err := c.sendAsync(cmd, b.cb); err != nil {
		t.Fatalf("sendAsync: %v", err)
	}
	if err := waitFor(t, b.first, "the timeout settling the request"); !errors.Is(err, ErrTimeout) {
		t.Fatalf("first settle got %v, want %v", err, ErrTimeout)
	}
	c.handle(&protocol.Reply{Id: cmd.Id})
	if n := b.count(); n != 1 {
		t.Fatalf("callback ran %d times, want 1: the reply settled a request that the timeout had settled", n)
	}
}

func TestClient_sendAsyncDropsLateReplyAfterClose(t *testing.T) {
	c, closeCh := newWritableClient(t, time.Hour)
	b := newBlockingCallback()
	defer close(b.release)
	cmd := &protocol.Command{Id: c.nextCmdID()}
	if err := c.sendAsync(cmd, b.cb); err != nil {
		t.Fatalf("sendAsync: %v", err)
	}
	close(closeCh)
	if err := waitFor(t, b.first, "the close settling the request"); !errors.Is(err, ErrClientDisconnected) {
		t.Fatalf("first settle got %v, want %v", err, ErrClientDisconnected)
	}
	c.handle(&protocol.Reply{Id: cmd.Id})
	if n := b.count(); n != 1 {
		t.Fatalf("callback ran %d times, want 1: the reply settled a request that the close had settled", n)
	}
}
