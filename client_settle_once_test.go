package centrifuge

// Request settlement tests from upstream centrifugal/centrifuge-go #127 and
// #142: a request callback runs exactly once, and a call whose send fails
// during a teardown returns.

import (
	"context"
	"errors"
	"io"
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
func TestClient_RequestCallbackNotInvokedTwiceOnTimeoutRace(t *testing.T) {
	config := Config{ReadTimeout: time.Microsecond}
	c := NewJsonClient("ws://localhost:9000/connection/websocket", config)
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
		// teardown fails every pending request, this one included.
		client.mu.Lock()
		client.clearConnectedState()
		client.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
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
