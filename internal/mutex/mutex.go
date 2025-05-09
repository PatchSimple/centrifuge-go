package mutex

import (
	"context"
)

// Mutex is like sync.Mutex. It offers the ability cancel waiting to obtain a
// lock by making use of Go's select statement.
//
// The zero value is not safe to use, and will cause a deadlock. You must
// initialize a Mutex before using the New() func.
type Mutex struct {
	// state must be a buffered mutex of 1.
	// locked: len(state) == 1
	// unlocked: len(state) == 0
	state chan struct{}
}

func New() *Mutex {
	return &Mutex{
		state: make(chan struct{}, 1),
	}
}

func (m *Mutex) waitLock() chan<- struct{} {
	return m.state
}

// WaitLock give access to internal state of the mutex and a send chan. do not
// close the chan, it will be cleaned up by gc. closing the channel will break
// the Mutex. Only use the chan it to send en empty struct to obtain the lock.
func (m *Mutex) WaitLock() chan<- struct{} {
	return m.waitLock()
}

// TryLockCtx is a convenience method for selecting WaitLock and ctx. If the ctx is
// done the ctx error will be returned.
func (m *Mutex) TryLockCtx(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case m.waitLock() <- struct{}{}:
		return nil
	}
}

// Lock locks m. If the lock is already in use, the calling goroutine blocks
// until the mutex is available.
func (m *Mutex) Lock() {
	m.waitLock() <- struct{}{}
}

// TryLock tries to lock m and reports whether it succeeded.
//
// Note that while correct uses of TryLock do exist, they are rare, and use of
// TryLock is often a sign of a deeper problem in a particular use of mutexes.
func (m *Mutex) TryLock() bool {
	select {
	case m.waitLock() <- struct{}{}:
		return true
	default:
		return false
	}
}

// Unlock unlocks m. It is a run-time error if m is not locked on entry to
// Unlock.
//
// Calling unlock on an unlocked lock is generally an indication of a race
// condition.
func (m *Mutex) Unlock() {
	select {
	case <-m.state:
	default:
		panic("unlock of unlocked mutex")
	}
}
