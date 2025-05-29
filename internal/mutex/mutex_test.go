package mutex

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// Helper to create a KamikazeMutex with a short timeout.
func newTestMutex() *KamikazeMutex {
	return NewKamikazeMutex(time.Minute)
}

func TestKamikazeMutex_LockUnlock(t *testing.T) {
	m := newTestMutex()
	m.Lock()
	m.Unlock() //nolint:staticcheck // Sis is intentional to test the mutex behavior
}

func TestKamikazeMutex_UnlockWithoutLockPanics(t *testing.T) {
	m := newTestMutex()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on Unlock without Lock")
		}
	}()
	m.Unlock()
}

func TestKamikazeMutex_LockTimeout_Exits(t *testing.T) {
	// This test runs a subprocess to check for os.Exit(1).
	code := `
package main
import (
	"time"
	"os"
	"github.com/centrifugal/centrifuge-go/internal/mutex"
)
func main() {
	m := mutex.NewKamikazeMutex(50 * time.Millisecond)
	m.Lock()
	go func() {
		// Hold the lock for longer than the timeout.
		time.Sleep(200 * time.Millisecond)
		m.Unlock()
	}()
	m.Lock() // should timeout and call os.Exit(1)
	os.Exit(0) // should not reach here
}
`
	tmp := t.TempDir() + "/main.go"
	if err := os.WriteFile(tmp, []byte(code), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "run", tmp)
	err := cmd.Run()
	if exitErr, ok := err.(*exec.ExitError); ok {
		if exitErr.ExitCode() != 1 {
			t.Fatalf("expected exit code 1, got %d", exitErr.ExitCode())
		}
	} else {
		t.Fatalf("expected process to exit with error, got %v", err)
	}
}

func TestKamikazeMutex_RLockRUnlock(t *testing.T) {
	m := newTestMutex()
	ok := m.RLock()
	if !ok {
		t.Fatal("RLock should return true")
	}
	m.RUnlock()
}

func TestKamikazeMutex_LockAfterUnlock(t *testing.T) {
	m := newTestMutex()
	m.Lock()
	m.Unlock() //nolint:staticcheck // Sis is intentional to test the mutex behavior
	m.Lock()
	m.Unlock() //nolint:staticcheck // Sis is intentional to test the mutex behavior
}
