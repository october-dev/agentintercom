//go:build linux || darwin

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/october-dev/october-bus/bus"
)

func TestScopeTokenWrapperHelper(t *testing.T) {
	raw := os.Getenv("BUS_WRAPPER_ARGS")
	if raw == "" {
		return
	}
	var args []string
	if json.Unmarshal([]byte(raw), &args) != nil {
		os.Exit(2)
	}
	if err := runAgent(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestScopeTokenWaitingChild(t *testing.T) {
	if os.Getenv("BUS_WAITING_CHILD") != "1" {
		return
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

// The operating system keeps reporting the environment a process was started
// with (Linux /proc/<pid>/environ, macOS KERN_PROCARGS2) to other processes of
// the same user, whatever the process later removes. A scope token passed by
// descriptor is never part of that record; one passed by environment is, for
// the launcher's whole life.
func TestAgentRunLaunchRecordHoldsNoDescriptorScopeToken(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runtimeValue, err := bus.Open(":memory:")
	requireNoError(t, err)
	server := bus.NewServer(runtimeValue, bus.ServerOptions{})
	address, err := server.Start()
	requireNoError(t, err)
	defer server.Stop(context.Background())
	scope, err := runtimeValue.CreateScope(ctx, bus.CreateScopeInput{ID: "launch-record"})
	requireNoError(t, err)
	owner := bus.Client{Address: address, Token: scope.ScopeToken}

	// launch starts agent run in its own process and reads its launch record
	// while its managed command is running.
	launch := func(id string, byDescriptor bool) ([]byte, error) {
		args := []string{"--id", id, "--name", id, "--address", address}
		env := removeEnvironment(os.Environ(), "OCTOBER_BUS_SCOPE_TOKEN", "OCTOBER_BUS_ADMIN_TOKEN", "OCTOBER_BUS_AGENT_TOKEN", "BUS_AUDIT_CHILD")
		wrapper := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestScopeTokenWrapperHelper$")
		var reader, writer *os.File
		if byDescriptor {
			var pipeErr error
			reader, writer, pipeErr = os.Pipe()
			requireNoError(t, pipeErr)
			wrapper.ExtraFiles = []*os.File{reader} // descriptor 3
			args = append(args, "--scope-token-fd", "3")
		} else {
			env = setEnvironment(env, "OCTOBER_BUS_SCOPE_TOKEN", scope.ScopeToken)
		}
		args = append(args, "--", os.Args[0], "-test.run=^TestScopeTokenWaitingChild$")
		encoded, err := json.Marshal(args)
		requireNoError(t, err)
		wrapper.Env = setEnvironment(env, "BUS_WRAPPER_ARGS", string(encoded), "BUS_WAITING_CHILD", "1")
		stdin, err := wrapper.StdinPipe()
		requireNoError(t, err)
		stderr := new(bytes.Buffer)
		wrapper.Stderr = stderr
		requireNoError(t, wrapper.Start())
		if byDescriptor {
			reader.Close()
			_, err = writer.WriteString(scope.ScopeToken + "\n")
			requireNoError(t, err)
			writer.Close()
		}
		registered := false
		for !registered && ctx.Err() == nil {
			agents, _ := owner.ListAgents(ctx)
			for _, agent := range agents {
				registered = registered || (agent.ID == id && agent.Reachable)
			}
			time.Sleep(20 * time.Millisecond)
		}
		record, recordErr := processLaunchRecord(wrapper.Process.Pid)
		stdin.Close()
		waitErr := wrapper.Wait()
		require(t, registered && waitErr == nil, "%s launch did not register and exit cleanly: %v; %s", id, waitErr, stderr.String())
		return record, recordErr
	}

	control, err := launch("environment", false)
	if err != nil {
		t.Skipf("this system does not report another process's launch record: %v", err)
	}
	require(t, bytes.Contains(control, []byte(scope.ScopeToken)), "control: an environment-launched agent run's record did not show its scope token, so this check reads nothing")
	record, err := launch("descriptor", true)
	requireNoError(t, err)
	require(t, len(record) > 0 && !bytes.Contains(record, []byte(scope.ScopeToken)), "the launch record of agent run --scope-token-fd holds the scope token")
}
