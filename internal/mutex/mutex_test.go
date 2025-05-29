package mutex

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Helper to create a KamikazeMutex with a short timeout.
func newTestMutex() *KamikazeMutex {
	return NewKamikazeMutex(time.Minute)
}

func TestKamikazeMutex_LockUnlock(t *testing.T) {
	m := newTestMutex()
	if len(m.x) != 0 {
		t.Fatalf("expected mutex to be unlocked, got %d", len(m.x))
	}
	m.Lock()
	if len(m.x) != 1 {
		t.Fatalf("expected mutex to be locked, got %d", len(m.x))
	}
	m.Unlock()
	if len(m.x) != 0 {
		t.Fatalf("expected mutex to be unlocked, got %d", len(m.x))
	}
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
	tmp := filepath.Join(t.TempDir(), "main.go")
	if err := os.WriteFile(tmp, []byte(code), 0644); err != nil {
		t.Fatalf("failed to write temporary file: %v", err)
	}
	cmd := exec.Command("go", "run", tmp)
	err := cmd.Run()
	exitErr := new(exec.ExitError)
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected process to exit with error, got %v", err)
	}
	if exitErr.ExitCode() != 1 {
		t.Fatalf("expected exit code 1, got %d", exitErr.ExitCode())
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
