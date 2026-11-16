package centrifuge

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/centrifugal/protocol"
)

var errTestWrite = errors.New("test write failed")

type fakeTransport struct {
	write func(cmd *protocol.Command, timeout time.Duration) error
}

func (f *fakeTransport) Read() (*protocol.Reply, *disconnect, error) {
	return nil, nil, io.EOF
}

func (f *fakeTransport) Write(cmd *protocol.Command, timeout time.Duration) error {
	return f.write(cmd, timeout)
}

func (f *fakeTransport) Close() error {
	return nil
}

func newTestClient(t *testing.T, state State, write func(*protocol.Command, time.Duration) error) *Client {
	t.Helper()
	c := NewJsonClient("ws://localhost:8000/connection/websocket", Config{})
	t.Cleanup(c.Close)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state = state
	if state == StateConnected {
		c.transport = &fakeTransport{write: write}
	}
	return c
}

func pendingRequests(c *Client) int {
	c.requestsMu.RLock()
	defer c.requestsMu.RUnlock()
	return len(c.requests)
}

func registeredCallback(c *Client, id uint32) func(*protocol.Reply, error) {
	c.requestsMu.RLock()
	defer c.requestsMu.RUnlock()
	return c.requests[id].cb
}

func TestClient_PublishReturnsErrClientClosed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newTestClient(t, StateDisconnected, nil)
		c.Close()
		_, err := c.Publish(t.Context(), "ch", []byte("data"))
		if !errors.Is(err, ErrClientClosed) {
			t.Fatalf("got %v, want ErrClientClosed", err)
		}
	})
}

func TestClient_PublishReturnsWriteError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var c *Client
		c = newTestClient(t, StateConnected, func(*protocol.Command, time.Duration) error {
			// Moving to connecting turns the teardown that send starts into a no-op.
			c.mu.Lock()
			defer c.mu.Unlock()
			c.state = StateConnecting
			return errTestWrite
		})
		_, err := c.Publish(t.Context(), "ch", []byte("data"))
		if !errors.Is(err, io.EOF) {
			t.Fatalf("got %v, want io.EOF", err)
		}
		synctest.Wait()
		if n := pendingRequests(c); n != 0 {
			t.Fatalf("got %d pending requests, want 0", n)
		}
	})
}

func TestClient_PublishReturnsErrClientDisconnectedWhenTeardownTakesRequest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var c *Client
		c = newTestClient(t, StateConnected, func(*protocol.Command, time.Duration) error {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.clearConnectedState()
			return errTestWrite
		})
		start := time.Now()
		_, err := c.Publish(t.Context(), "ch", []byte("data"))
		if !errors.Is(err, ErrClientDisconnected) {
			t.Fatalf("got %v, want ErrClientDisconnected", err)
		}
		if d := time.Since(start); d != 0 {
			t.Fatalf("Publish took %v, want no wait", d)
		}
		synctest.Wait()
		if n := pendingRequests(c); n != 0 {
			t.Fatalf("got %d pending requests, want 0", n)
		}
	})
}

func TestClient_PublishSucceedsOnReply(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		written := make(chan uint32, 1)
		c := newTestClient(t, StateConnected, func(cmd *protocol.Command, _ time.Duration) error {
			written <- cmd.Id
			return nil
		})
		var err error
		var wg sync.WaitGroup
		wg.Go(func() {
			_, err = c.Publish(t.Context(), "ch", []byte("data"))
		})
		c.handle(&protocol.Reply{Id: <-written})
		wg.Wait()
		if err != nil {
			t.Fatalf("got %v, want nil", err)
		}
		if n := pendingRequests(c); n != 0 {
			t.Fatalf("got %d pending requests, want 0", n)
		}
	})
}

func TestClient_PublishReturnsServerError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		written := make(chan uint32, 1)
		c := newTestClient(t, StateConnected, func(cmd *protocol.Command, _ time.Duration) error {
			written <- cmd.Id
			return nil
		})
		var err error
		var wg sync.WaitGroup
		wg.Go(func() {
			_, err = c.Publish(t.Context(), "ch", []byte("data"))
		})
		c.handle(&protocol.Reply{Id: <-written, Error: &protocol.Error{Code: 103, Message: "permission denied"}})
		wg.Wait()
		var srvErr *Error
		if !errors.As(err, &srvErr) || srvErr.Code != 103 {
			t.Fatalf("got %v, want server error 103", err)
		}
	})
}

func TestClient_PublishReturnsPromptlyOnCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		written := make(chan uint32, 1)
		c := newTestClient(t, StateConnected, func(cmd *protocol.Command, _ time.Duration) error {
			written <- cmd.Id
			return nil
		})
		ctx, cancel := context.WithCancel(t.Context())
		var err error
		var wg sync.WaitGroup
		wg.Go(func() {
			_, err = c.Publish(ctx, "ch", []byte("data"))
		})
		id := <-written
		start := time.Now()
		cancel()
		wg.Wait()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want context.Canceled", err)
		}
		if d := time.Since(start); d != 0 {
			t.Fatalf("Publish took %v after cancel, want no wait", d)
		}
		if n := pendingRequests(c); n != 0 {
			t.Fatalf("got %d pending requests, want 0", n)
		}
		c.handle(&protocol.Reply{Id: id})
		c.mu.Lock()
		c.clearConnectedState()
		c.mu.Unlock()
		synctest.Wait()
	})
}

func TestClient_PublishTimesOutAfterReadTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		writeTimeout := make(chan time.Duration, 1)
		c := newTestClient(t, StateConnected, func(_ *protocol.Command, timeout time.Duration) error {
			writeTimeout <- timeout
			return nil
		})
		start := time.Now()
		_, err := c.Publish(t.Context(), "ch", []byte("data"))
		if !errors.Is(err, ErrTimeout) {
			t.Fatalf("got %v, want ErrTimeout", err)
		}
		if d := time.Since(start); d != c.config.ReadTimeout {
			t.Fatalf("Publish took %v, want ReadTimeout %v", d, c.config.ReadTimeout)
		}
		if got := <-writeTimeout; got != c.config.WriteTimeout {
			t.Fatalf("write timeout %v, want WriteTimeout %v", got, c.config.WriteTimeout)
		}
		if n := pendingRequests(c); n != 0 {
			t.Fatalf("got %d pending requests, want 0", n)
		}
	})
}

func TestClient_PublishBoundsWriteByContextDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		writeTimeout := make(chan time.Duration, 1)
		c := newTestClient(t, StateConnected, func(_ *protocol.Command, timeout time.Duration) error {
			writeTimeout <- timeout
			return nil
		})
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()
		_, err := c.Publish(ctx, "ch", []byte("data"))
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("got %v, want context.DeadlineExceeded", err)
		}
		if got := <-writeTimeout; got != 100*time.Millisecond {
			t.Fatalf("write timeout %v, want 100ms", got)
		}
	})
}

func TestClient_PublishCallbackNeverBlocks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var c *Client
		registered := make(chan func(*protocol.Reply, error), 1)
		c = newTestClient(t, StateConnected, func(cmd *protocol.Command, _ time.Duration) error {
			registered <- registeredCallback(c, cmd.Id)
			return nil
		})
		var err error
		var wg sync.WaitGroup
		wg.Go(func() {
			_, err = c.Publish(t.Context(), "ch", []byte("data"))
		})
		cb := <-registered
		cb(&protocol.Reply{}, nil)
		cb(nil, ErrClientDisconnected)
		wg.Wait()
		if err != nil {
			t.Fatalf("got %v, want the first result, nil", err)
		}
		cb(nil, ErrClientDisconnected)
	})
}

func TestClient_PublishWaitsForConnect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newTestClient(t, StateConnecting, nil)
		var err error
		var wg sync.WaitGroup
		wg.Go(func() {
			_, err = c.Publish(t.Context(), "ch", []byte("data"))
		})
		synctest.Wait()
		c.mu.Lock()
		c.state = StateConnected
		c.transport = &fakeTransport{write: func(cmd *protocol.Command, _ time.Duration) error {
			c.handle(&protocol.Reply{Id: cmd.Id})
			return nil
		}}
		c.resolveConnectFutures(nil)
		c.mu.Unlock()
		wg.Wait()
		if err != nil {
			t.Fatalf("got %v, want nil", err)
		}
	})
}

func TestClient_PublishReturnsPromptlyOnCancelWhileConnecting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newTestClient(t, StateConnecting, nil)
		ctx, cancel := context.WithCancel(t.Context())
		var err error
		var wg sync.WaitGroup
		wg.Go(func() {
			_, err = c.Publish(ctx, "ch", []byte("data"))
		})
		synctest.Wait()
		start := time.Now()
		cancel()
		wg.Wait()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want context.Canceled", err)
		}
		if d := time.Since(start); d != 0 {
			t.Fatalf("Publish took %v after cancel, want no wait", d)
		}
	})
}

func TestClient_PublishChecksContextAfterConnect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		writes := 0
		c := newTestClient(t, StateConnected, func(*protocol.Command, time.Duration) error {
			writes++
			return nil
		})
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := c.Publish(ctx, "ch", []byte("data"))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want context.Canceled", err)
		}
		if writes != 0 {
			t.Fatalf("got %d writes, want 0", writes)
		}
	})
}

func TestClient_PublishReturnsContextErrorWhenDisconnected(t *testing.T) {
	c := newTestClient(t, StateDisconnected, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	// The connect result and ctx.Done() are both ready, so select picks one at
	// random. Repeating makes a wrong pick show up.
	for range 100 {
		_, err := c.Publish(ctx, "ch", []byte("data"))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want context.Canceled", err)
		}
	}
}

func TestClient_PublishWritesWithNegativeWriteTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var c *Client
		writeTimeout := make(chan time.Duration, 1)
		c = newTestClient(t, StateConnected, func(cmd *protocol.Command, timeout time.Duration) error {
			writeTimeout <- timeout
			c.handle(&protocol.Reply{Id: cmd.Id})
			return nil
		})
		c.config.WriteTimeout = -1
		_, err := c.Publish(t.Context(), "ch", []byte("data"))
		if err != nil {
			t.Fatalf("got %v, want nil", err)
		}
		if got := <-writeTimeout; got != c.config.ReadTimeout {
			t.Fatalf("write timeout %v, want ReadTimeout %v", got, c.config.ReadTimeout)
		}
	})
}

func TestClient_PublishReadsTransportUnderLock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newTestClient(t, StateConnecting, nil)
		var err error
		var wg sync.WaitGroup
		wg.Go(func() {
			_, err = c.Publish(t.Context(), "ch", []byte("data"))
		})
		synctest.Wait()
		c.mu.Lock()
		c.state = StateConnected
		c.transport = &fakeTransport{write: func(*protocol.Command, time.Duration) error { return nil }}
		c.resolveConnectFutures(nil)
		c.mu.Unlock()
		// Publish resumes after the connect, so this write races with its read
		// of the transport unless that read holds c.mu. Run with -race.
		c.mu.Lock()
		c.transport = nil
		c.mu.Unlock()
		wg.Wait()
		if err == nil {
			t.Fatal("got nil, want an error")
		}
	})
}

func Test_writeTimeout(t *testing.T) {
	cases := []struct {
		name     string
		deadline time.Duration
		limit    time.Duration
		want     time.Duration
		wantOK   bool
	}{
		{"limit_comes_first", 5 * time.Second, time.Second, time.Second, true},
		{"deadline_comes_first", time.Second, 5 * time.Second, time.Second, true},
		{"negative_limit_leaves_deadline", 5 * time.Second, -1, 5 * time.Second, true},
		{"deadline_now_skips_write", 0, time.Second, 0, false},
		{"passed_deadline_skips_write", -time.Second, time.Second, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				got, ok := writeTimeout(time.Now().Add(tc.deadline), tc.limit)
				if got != tc.want || ok != tc.wantOK {
					t.Fatalf("got (%v, %v), want (%v, %v)", got, ok, tc.want, tc.wantOK)
				}
			})
		})
	}
}
