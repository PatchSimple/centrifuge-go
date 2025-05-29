package mutex

import (
	"log/slog"
	"os"
	"runtime/debug"
	"time"
)

// KamikazeMutex is a drop-in replacement for sync.Mutex. It calls os.Exit if a
// lock cannot be acquired within the specified timeout.
type KamikazeMutex struct {
	x       chan struct{} // If len(x) == 1, then the mutex is locked.
	timeout time.Duration
}

func NewKamikazeMutex(timeout time.Duration) *KamikazeMutex {
	return &KamikazeMutex{
		x:       make(chan struct{}, 1),
		timeout: timeout,
	}
}

// Lock will block until the lock can be acquired or the timeout is reached. If
// the timeout is reached, it calls os.Exit.
func (m *KamikazeMutex) Lock() {
	select {
	case <-time.After(m.timeout):
		stackTrace := debug.Stack()
		slog.Error("deadlock detected",
			"timeout", m.timeout.String(),
			"stack_trace", stackTrace,
		)
		os.Exit(1)
	case m.x <- struct{}{}:
		// Lock acquired.
	}
}

func (m *KamikazeMutex) Unlock() {
	select {
	case <-m.x:
	default:
		panic("mutex is not locked")
	}
}

// RLock is a placeholder for compatibility. It is functionally equivalent to a
// normal lock.
func (m *KamikazeMutex) RLock() bool {
	m.Lock()
	return true
}

// RUnlock is a placeholder for compatibility. It is functionally equivalent to
// a normal unlock.
func (m *KamikazeMutex) RUnlock() {
	m.Unlock()
}
