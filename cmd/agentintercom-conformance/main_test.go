package main

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/october-dev/october-bus/bus"
)

func TestIsolatedRuntimeCleanup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	runtimeValue, err := startIsolatedRuntime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	root := runtimeValue.root
	if _, err := (bus.Client{Address: runtimeValue.daemon.RunFile.Address}).Health(ctx); err != nil {
		t.Fatal(err)
	}
	if err := runtimeValue.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary runtime directory still exists: %v", err)
	}
}
