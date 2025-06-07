//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func createTokenScript(t *testing.T, uniqueString string) string {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "token.sh")
	scriptContent := "#!/bin/sh\necho '" + uniqueString + "'\n"
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0700); err != nil {
		t.Fatalf("failed to write script: %v", err)
	}
	if err := os.Chmod(scriptPath, 0700); err != nil {
		t.Fatalf("failed to chmod script: %v", err)
	}
	return scriptPath
}
