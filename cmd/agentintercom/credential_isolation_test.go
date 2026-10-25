package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/october-dev/october-bus/bus"
)

func TestAuditCredentialChild(t *testing.T) {
	if os.Getenv("BUS_AUDIT_CHILD") != "1" {
		return
	}
	// Report only presence. Never print a credential value.
	for _, name := range []string{"OCTOBER_BUS_ADMIN_TOKEN", "OCTOBER_BUS_SCOPE_TOKEN", "BUS_AUDIT_SCOPE_TOKEN"} {
		if os.Getenv(name) != "" {
			t.Errorf("managed child inherited privileged variable %s", name)
		}
	}
}

func TestManagedLauncherUsesPrivateScopeCache(t *testing.T) {
	root := t.TempDir()
	t.Setenv("OCTOBER_BUS_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("OCTOBER_BUS_RUNTIME_DIR", filepath.Join(root, "run"))
	t.Setenv("OCTOBER_BUS_SCOPE_TOKEN", "")
	t.Setenv("OCTOBER_BUS_AGENT_TOKEN", "")
	t.Setenv("BUS_AUDIT_SCOPE_TOKEN", "")
	t.Setenv("OCTOBER_BUS_ADDRESS", "http://127.0.0.1:1") // local mode ignores ambient routing
	ctx := context.Background()
	daemon, err := bus.StartDaemon(ctx, 0, nil)
	requireNoError(t, err)
	defer daemon.Stop(ctx)
	var output bytes.Buffer
	requireNoError(t, captureStdout(&output, func() error { return createScope("native") }))
	t.Setenv("OCTOBER_BUS_ADMIN_TOKEN", "synthetic-admin-marker")
	t.Setenv("BUS_AUDIT_CHILD", "1")
	args := []string{"--scope", "native", "--id", "native", "--name", "Native", "--", os.Args[0], "-test.run=^TestAuditCredentialChild$"}
	requireNoError(t, runAgent(args))
	owner, err := localScopeClient("native")
	requireNoError(t, err)
	agents, err := owner.ListAgents(ctx)
	require(t, err == nil && len(agents) == 1 && !agents[0].Reachable && agents[0].Lifecycle == bus.LifecycleOffline, "cached launcher did not retire: %v", err)
	t.Setenv("OCTOBER_BUS_SCOPE_TOKEN", "synthetic-mixed-authority")
	require(t, runAgent(args) != nil, "accepted mixed cached/environment authority")
	t.Setenv("OCTOBER_BUS_SCOPE_TOKEN", "")
	args[1] = "missing"
	require(t, runAgent(args) != nil, "registered without cached scope authority")
}

func TestAuditManagedChildStripsPrivilegedCredentials(t *testing.T) {
	if os.Getenv("BUS_AUDIT_CHILD") == "1" {
		return
	}
	ctx := context.Background()
	runtime, err := bus.Open(":memory:")
	requireNoError(t, err)
	server := bus.NewServer(runtime, bus.ServerOptions{})
	address, err := server.Start()
	requireNoError(t, err)
	defer server.Stop(ctx)
	scope, err := runtime.CreateScope(ctx, bus.CreateScopeInput{ID: "audit"})
	requireNoError(t, err)
	t.Setenv("BUS_AUDIT_SCOPE_TOKEN", scope.ScopeToken)
	t.Setenv("OCTOBER_BUS_SCOPE_TOKEN", "synthetic-privileged-scope-marker")
	t.Setenv("OCTOBER_BUS_ADMIN_TOKEN", "synthetic-admin-marker")
	t.Setenv("BUS_AUDIT_CHILD", "1")
	err = runAgent([]string{"--id", "worker", "--name", "Worker", "--address", address, "--scope-token-env", "BUS_AUDIT_SCOPE_TOKEN", "--", os.Args[0], "-test.run=^TestAuditCredentialChild$"})
	require(t, err == nil, "credential isolation check failed: %v", err)
}
