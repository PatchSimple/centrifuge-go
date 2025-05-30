package centrifuge

import (
	"context"
	"sync/atomic"
	"time"
)

// cbQueue allows processing callbacks in separate goroutine with
// preserved order.
type cbQueue struct {
	callbacks *List[*asyncCB] // Using a List to preserve order and allow blocking operations.
	closeCh   chan struct{}   // Channel to signal that the queue is closed.
	closed    atomic.Bool     // Atomic boolean to check if the queue is closed.
}

func newCBQueue(buffSize int) *cbQueue {
	return &cbQueue{
		callbacks: NewList[*asyncCB](),
		closeCh:   make(chan struct{}),
	}
}

type asyncCB struct {
	ready chan chan struct{} // Channel to signal that the callback is ready to be executed.
}

// dispatch is responsible for calling async callbacks. Should be run
// in separate goroutine.
func (q *cbQueue) dispatch() {
	for {
		select {
		case <-q.closeCh:
			return
		default:
			q.dispatchOne()
		}
	}
}

func (q *cbQueue) dispatchOne() {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer cancel()
		<-q.closeCh
	}()
	v, err := q.callbacks.PopFrontCtx(ctx)
	if err != nil {
		return
	}
	// signal that we are ready to execute the callback
	done := make(chan struct{})
	select {
	case <-ctx.Done():
		return
	case v.ready <- done:
	}

	// wait for fn to finish
	select {
	case <-ctx.Done():
		return
	case <-done:
	}
}

// Push adds the given function to the tail of the list and
// signals the dispatcher.
func (q *cbQueue) push(f func(duration time.Duration)) {
	select {
	case <-q.closeCh:
		return
	default:
	}
	start := time.Now()
	cb := &asyncCB{ready: make(chan chan struct{}, 1)}
	q.callbacks.PushBack(cb)
	if done, ok := <-cb.ready; ok {
		f(time.Since(start))
		close(done)
	}
}

// Close signals that async queue must be closed.
// Queue won't accept any more callbacks after that – ignoring them if pushed.
func (q *cbQueue) close() {
	if q.closed.Swap(true) {
		return // Already closed, do nothing.
	}
	close(q.closeCh)
	// Drain the queue to ensure all callbacks are processed before closing.
	for q.callbacks.Len() > 0 {
		q.dispatchOne()
	}
}
