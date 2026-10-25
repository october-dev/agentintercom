package conformance_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/october-dev/october-bus/bus"
	"github.com/october-dev/october-bus/conformance"
)

func buildRuntimeCommand(t *testing.T) string {
	t.Helper()
	name := "agentintercom"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(t.TempDir(), name)
	command := exec.Command("go", "build", "-o", path, "../cmd/agentintercom")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build runtime command: %v\n%s", err, output)
	}
	return path
}

func TestLocalRuntimeProfile(t *testing.T) {
	runtimeValue, err := bus.Open(":memory:")
	requireNoError(t, err)
	server := bus.NewServer(runtimeValue, bus.ServerOptions{AdminToken: "conformance-admin-token"})
	address, err := server.Start()
	requireNoError(t, err)
	defer server.Stop(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := conformance.Run(ctx, conformance.Options{Address: address, AdminToken: "conformance-admin-token"})
	requireNoError(t, err)
	require(t, result.Profile == conformance.ProfileLocalRuntime && result.ProtocolVersion == bus.ProtocolVersion && !(len(result.Passed) < 10) && len(result.Failed) == 0 && result.CompletedAt != "", "unexpected result: %#v", result)
}

func TestFailureResultNamesTheFailedCheck(t *testing.T) {
	runtimeValue, err := bus.Open(":memory:")
	requireNoError(t, err)
	server := bus.NewServer(runtimeValue, bus.ServerOptions{AdminToken: "correct-token"})
	address, err := server.Start()
	requireNoError(t, err)
	defer server.Stop(context.Background())
	result, err := conformance.Run(context.Background(), conformance.Options{Address: address, AdminToken: "wrong-token"})
	require(t, err != nil && len(result.Failed) == 1 && result.Failed[0].Check == "scope-authority" && result.CompletedAt != "", "unexpected failure result: %#v, %v", result, err)
}

func TestMCPAdapterProfile(t *testing.T) {
	paths := bus.DaemonPaths{DataDir: t.TempDir(), RuntimeDir: t.TempDir()}
	paths.Database = filepath.Join(paths.DataDir, "bus.sqlite")
	paths.RunFile = filepath.Join(paths.RuntimeDir, "bus.json")
	paths.LockFile = filepath.Join(paths.RuntimeDir, "daemon.lock")
	daemon, err := bus.StartDaemon(context.Background(), 0, &paths)
	requireNoError(t, err)
	defer daemon.Stop(context.Background())
	binary := buildRuntimeCommand(t)
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	result, err := conformance.RunMCPAdapter(ctx, conformance.MCPAdapterOptions{
		Address: daemon.RunFile.Address, AdminToken: daemon.RunFile.AdminToken, Command: binary, Args: []string{"mcp", "stdio"}, LocalPaths: &paths,
	})
	requireNoError(t, err)
	require(t, result.Profile == conformance.ProfileMCPAdapter && result.ProtocolVersion == bus.ProtocolVersion && len(result.Passed) == 15 && len(result.Failed) == 0 && result.CompletedAt != "", "unexpected result: %#v", result)
}

func TestMCPAdapterFailureNamesStartupCheck(t *testing.T) {
	runtimeValue, err := bus.Open(":memory:")
	requireNoError(t, err)
	server := bus.NewServer(runtimeValue, bus.ServerOptions{AdminToken: "conformance-admin-token"})
	address, err := server.Start()
	requireNoError(t, err)
	defer server.Stop(context.Background())
	result, err := conformance.RunMCPAdapter(context.Background(), conformance.MCPAdapterOptions{
		Address: address, AdminToken: "conformance-admin-token", Command: buildRuntimeCommand(t), Args: []string{"version"},
	})
	require(t, err != nil && len(result.Failed) == 1 && result.Failed[0].Check == "adapter-start-and-execution-identity" && result.CompletedAt != "", "unexpected failure result: %#v, %v", result, err)
}
