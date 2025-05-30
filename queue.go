package centrifuge

import (
	"sync"
	"sync/atomic"
	"time"
)

// cbQueue allows processing callbacks in separate goroutine with
// preserved order.
type cbQueue struct {
	callbacks chan *asyncCB
	mu        sync.Mutex
	closed    atomic.Bool
}

func newCBQueue(buffSize int) *cbQueue {
	return &cbQueue{
		callbacks: make(chan *asyncCB, buffSize),
	}
}

type asyncCB struct {
	fn func(delay time.Duration)
	tm time.Time
}

// dispatch is responsible for calling async callbacks. Should be run
// in separate goroutine.
func (q *cbQueue) dispatch() {
	for cb := range q.callbacks {
		if cb == nil {
			continue
		}
		delay := time.Since(cb.tm)
		cb.fn(delay)
	}
}

// Push adds the given function to the tail of the list and
// signals the dispatcher.
func (q *cbQueue) push(f func(duration time.Duration)) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed.Load() {
		// If queue is closed, we ignore the callback.
		return
	}
	cb := &asyncCB{fn: f, tm: time.Now()}
	q.callbacks <- cb
}

// Close signals that async queue must be closed.
// Queue won't accept any more callbacks after that – ignoring them if pushed.
func (q *cbQueue) close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed.Load() {
		return
	}
	q.closed.Store(true)
	close(q.callbacks)
}
