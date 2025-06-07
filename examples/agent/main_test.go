package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunGroup_AllSuccess(t *testing.T) {
	var called1, called2 atomic.Bool
	ctx := context.Background()
	err := runGroup(ctx,
		func(ctx context.Context) error {
			called1.Store(true)
			return nil
		},
		func(ctx context.Context) error {
			called2.Store(true)
			<-ctx.Done()
			return nil
		},
	)
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
	if !called1.Load() || !called2.Load() {
		t.Error("expected both functions to be called")
	}
}

func TestRunGroup_OneError(t *testing.T) {
	errTest := errors.New("test error")
	ctx := context.Background()
	err := runGroup(ctx,
		func(ctx context.Context) error {
			return errTest
		},
		func(ctx context.Context) error {
			return nil
		},
	)
	if !errors.Is(err, errTest) {
		t.Errorf("expected error to contain test error, got %v", err)
	}
}

func TestRunGroup_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan struct{})
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	err := runGroup(ctx,
		func(ctx context.Context) error {
			<-ctx.Done()
			close(ch)
			return ctx.Err()
		},
	)
	<-ch
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestRunGroup_PanicRecovery(t *testing.T) {
	ctx := context.Background()
	err := runGroup(ctx,
		func(ctx context.Context) error {
			panic("panic in goroutine")
		},
	)
	if err == nil {
		t.Error("expected error due to panic, got nil")
	}
}

func TestRunGroupFaultTolerant_CancelsOnContext(t *testing.T) {
	var called atomic.Bool
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	err := runGroupFaultTolerant(ctx, func(ctx context.Context) error {
		<-ctx.Done()
		called.Store(true)
		return ctx.Err()
	})
	if !called.Load() {
		t.Error("expected function to be called before context cancel")
	}
	if !errors.Is(err, context.Canceled) && err != nil {
		t.Errorf("expected context.Canceled or nil, got %v", err)
	}
}

func TestRunGroupFaultTolerant_RestartsOnError(t *testing.T) {
	var count atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	err := runGroupFaultTolerant(ctx, func(ctx context.Context) error {
		if count.Add(1) >= 3 {
			cancel()
		}
		return errors.New("fail and restart")
	})
	if count.Load() != 3 {
		t.Errorf("expected function to be restarted exactly 3 times, got %d", count.Load())
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled or nil, got %v", err)
	}
}

// createTokenScript is now split into OS-specific files.

func TestRunTokenExec_ReturnsExpectedString(t *testing.T) {
	uniqueString := "token-1234-unique-test-value"
	scriptPath := createTokenScript(t, uniqueString)

	output, err := runTokenExec(scriptPath)
	if err != nil {
		t.Fatalf("runTokenExec failed: %v", err)
	}
	if strings.TrimSpace(output) != uniqueString {
		t.Errorf("expected %q, got %q", uniqueString, output)
	}
}

func TestRunTokenExec_CommandFails(t *testing.T) {
	// Use a non-existent file path
	badPath := filepath.Join(t.TempDir(), "does_not_exist.sh")
	_, err := runTokenExec(badPath)
	if err == nil {
		t.Error("expected error for non-existent executable, got nil")
	}
}
