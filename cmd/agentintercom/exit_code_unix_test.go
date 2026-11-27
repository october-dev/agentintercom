//go:build darwin || linux

package main

import (
	"errors"
	"fmt"
	"os/exec"
	"testing"
)

func TestExitCodePassesManagedChildStatusThrough(t *testing.T) {
	err := exec.Command("sh", "-c", "exit 7").Run()
	if code := exitCode(fmt.Errorf("wrapped: %w", err)); code != 7 {
		t.Fatalf("child exit status became %d", code)
	}
	if code := exitCode(errors.New("refused")); code != 1 {
		t.Fatalf("ordinary failure became %d", code)
	}
}
