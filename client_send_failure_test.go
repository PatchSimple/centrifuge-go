package centrifuge

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/centrifugal/protocol"
)

// deadTransport models a websocket that died while the host slept: the client still
// believes it is connected, and every write fails. onWrite lets a test reproduce the
// reader goroutine discovering the same dead socket at the same moment.
type deadTransport struct {
	onWrite func()
}

func (deadTransport) Read() (*protocol.Reply, *disconnect, error) {
	select {} // never delivers a reply
}

func (t deadTransport) Write(*protocol.Command, time.Duration) error {
	if t.onWrite != nil {
		t.onWrite()
	}
	return errors.New("wsasend: an existing connection was forcibly closed")
}

func (deadTransport) Close() error { return nil }

func newDeadTransportClient(onWrite func(c *Client)) *Client {
	c := NewJsonClient("ws://127.0.0.1:1/connection", Config{
		ReadTimeout:  30 * time.Second,
		WriteTimeout: time.Second,
	})
	c.mu.Lock()
	c.state = StateConnected
	c.transport = deadTransport{onWrite: func() {
		if onWrite != nil {
			onWrite(c)
		}
	}}
	c.closeCh = make(chan struct{})
	c.mu.Unlock()
	return c
}

// publishReturns reports whether Publish returned within its context deadline plus a
// margin.
func publishReturns(c *Client) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := c.Publish(ctx, "test", []byte(`{}`))
		done <- err
	}()
	select {
	case <-done:
		return true
	case <-time.After(2300 * time.Millisecond):
		return false
	}
}

func TestPublishReturnsWhenWriteFails(t *testing.T) {
	c := newDeadTransportClient(nil)
	defer c.Close()
	if !publishReturns(c) {
		t.Fatal("Publish did not return")
	}
}

// A teardown that clears pending requests while the publishing goroutine is still in
// the failing write used to run the publish callback a second time. Publish waits for a
// single result, so the caller blocked forever before reaching its ctx select.
func TestPublishReturnsWhenTeardownRacesFailedWrite(t *testing.T) {
	c := newDeadTransportClient(func(c *Client) {
		go c.handleDisconnect(&disconnect{Code: connectingTransportClosed, Reason: "transport closed", Reconnect: true})
		time.Sleep(50 * time.Millisecond) // let the teardown settle the request first
	})
	defer c.Close()
	if !publishReturns(c) {
		t.Fatal("Publish never returned: its callback ran twice and the context deadline was ignored")
	}
}
