//go:build unix

package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/october-dev/october-bus/bus"
	"golang.org/x/sys/unix"
)

// scopeTokenPipe returns the read end of a pipe holding data, left open across
// exec as a launcher's descriptor would be, so a reader that forgot to close it
// would hand it to the managed command. The write end stays open only when
// asked; close it with the returned function.
func scopeTokenPipe(t *testing.T, data string, keepWriter bool) (int, func()) {
	t.Helper()
	var fds [2]int
	syscall.ForkLock.RLock()
	err := syscall.Pipe(fds[:])
	if err == nil {
		syscall.CloseOnExec(fds[0])
		syscall.CloseOnExec(fds[1])
	}
	syscall.ForkLock.RUnlock()
	requireNoError(t, err)
	if data != "" {
		_, err = syscall.Write(fds[1], []byte(data))
		requireNoError(t, err)
	}
	closeWriter := func() { _ = syscall.Close(fds[1]) }
	if !keepWriter {
		closeWriter()
		closeWriter = func() {}
	}
	_, err = unix.FcntlInt(uintptr(fds[0]), unix.F_SETFD, 0)
	requireNoError(t, err)
	return fds[0], closeWriter
}

// descriptorIdentity names an open descriptor by number and file, so a later
// check is not fooled by an unrelated file reusing the number.
func descriptorIdentity(fd int) string {
	var stat syscall.Stat_t
	if syscall.Fstat(fd, &stat) != nil {
		return ""
	}
	return fmt.Sprintf("%d:%d:%d", fd, stat.Dev, stat.Ino)
}

func TestScopeTokenFDChild(t *testing.T) {
	if os.Getenv("BUS_AUDIT_CHILD") != "1" {
		return
	}
	auditScopeTokenChild(t)
	identity := os.Getenv("BUS_AUDIT_FD")
	number, _, _ := strings.Cut(identity, ":")
	if fd, err := strconv.Atoi(number); err != nil || descriptorIdentity(fd) == identity {
		t.Errorf("managed child inherited the scope token descriptor")
	}
}

func TestAgentRunReadsScopeTokenFromDescriptor(t *testing.T) {
	ctx := context.Background()
	runtimeValue, err := bus.Open(":memory:")
	requireNoError(t, err)
	server := bus.NewServer(runtimeValue, bus.ServerOptions{})
	address, err := server.Start()
	requireNoError(t, err)
	defer server.Stop(ctx)
	scope, err := runtimeValue.CreateScope(ctx, bus.CreateScopeInput{ID: "token-fd"})
	requireNoError(t, err)
	fd, _ := scopeTokenPipe(t, scope.ScopeToken+"\n", false)
	identity := descriptorIdentity(fd)
	t.Setenv("OCTOBER_BUS_SCOPE_TOKEN", "")
	t.Setenv("OCTOBER_BUS_ADMIN_TOKEN", "synthetic-admin-marker")
	t.Setenv("BUS_AUDIT_CHILD", "1")
	t.Setenv("BUS_AUDIT_SCOPE_DIGEST", scopeTokenDigest(scope.ScopeToken))
	t.Setenv("BUS_AUDIT_FD", identity)
	requireNoError(t, runAgent([]string{
		"--id", "worker", "--name", "Worker", "--address", address, "--scope-token-fd", strconv.Itoa(fd),
		"--", os.Args[0], "-test.run=^TestScopeTokenFDChild$",
	}))
	require(t, descriptorIdentity(fd) != identity, "agent run left the scope token descriptor open")
	agents, err := (bus.Client{Address: address, Token: scope.ScopeToken}).ListAgents(ctx)
	require(t, err == nil && len(agents) == 1 && agents[0].ID == "worker" && agents[0].Lifecycle == bus.LifecycleOffline,
		"descriptor-launched agent did not register and retire: %#v, %v", agents, err)
}

func TestAgentRunRefusesMixedScopeTokenSources(t *testing.T) {
	token := "synthetic-scope-token-marker"
	fd, _ := scopeTokenPipe(t, token, false)
	defer syscall.Close(fd)
	file := t.TempDir() + "/scope.token"
	requireNoError(t, os.WriteFile(file, []byte(token), 0o600))
	t.Setenv("OCTOBER_BUS_SCOPE_TOKEN", "")
	t.Setenv("BUS_ALTERNATE_SCOPE_TOKEN", "")
	descriptor := strconv.Itoa(fd)
	for _, testCase := range []struct {
		env, value string
		args       []string
		want       string
	}{
		{args: []string{"--scope", "local", "--scope-token-fd", descriptor}, want: "use only one of"},
		{args: []string{"--scope-token-file", file, "--scope-token-fd", descriptor}, want: "use only one of"},
		{env: "OCTOBER_BUS_SCOPE_TOKEN", value: "inherited", args: []string{"--scope-token-fd", descriptor}, want: "unset OCTOBER_BUS_SCOPE_TOKEN"},
		{env: "BUS_ALTERNATE_SCOPE_TOKEN", value: "inherited", args: []string{"--scope-token-env", "BUS_ALTERNATE_SCOPE_TOKEN", "--scope-token-fd", descriptor}, want: "unset BUS_ALTERNATE_SCOPE_TOKEN"},
	} {
		if testCase.env != "" {
			t.Setenv(testCase.env, testCase.value)
		}
		args := append(append([]string{"--id", "worker", "--name", "Worker", "--address", "http://127.0.0.1:1"}, testCase.args...), "--", "true")
		err := runAgent(args)
		require(t, err != nil && strings.Contains(err.Error(), testCase.want), "%v: want %q, got %v", testCase.args, testCase.want, err)
		if testCase.env != "" {
			t.Setenv(testCase.env, "")
		}
	}
	// A refused launch never read the descriptor: the token is still there.
	buffer := make([]byte, 64)
	count, err := syscall.Read(fd, buffer)
	require(t, err == nil && string(buffer[:count]) == token, "a refused launch consumed the descriptor: %v", err)
}

func TestReadScopeTokenFDBoundsAndCloses(t *testing.T) {
	token := strings.Repeat("A", 42) + "Q"
	for _, fd := range []int{0, 1, 2, -1} {
		_, err := readScopeTokenFD(fd)
		require(t, err != nil && strings.Contains(err.Error(), "3 or more"), "accepted descriptor %d: %v", fd, err)
	}
	require(t, descriptorIdentity(0) != "", "a refused standard descriptor was closed")

	for name, data := range map[string]string{
		"empty":     "",
		"malformed": "not-a-scope-token\n",
		"oversized": strings.Repeat("A", maxScopeTokenBytes+1),
	} {
		fd, _ := scopeTokenPipe(t, data, false)
		identity := descriptorIdentity(fd)
		_, err := readScopeTokenFD(fd)
		require(t, err != nil, "%s descriptor accepted", name)
		require(t, descriptorIdentity(fd) != identity, "%s descriptor left open", name)
	}

	// A newline ends the token, so a writer that keeps its end open does not
	// block the launcher, and anything after the newline is ignored.
	fd, closeWriter := scopeTokenPipe(t, token+"\nignored", true)
	defer closeWriter()
	result := make(chan error, 1)
	go func() {
		got, err := readScopeTokenFD(fd)
		if err == nil && got != token {
			err = fmt.Errorf("read the wrong token")
		}
		result <- err
	}()
	select {
	case err := <-result:
		requireNoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("reading a newline-terminated token waited for end of file")
	}
}
