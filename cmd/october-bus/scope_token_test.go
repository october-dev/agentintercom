package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/october-dev/october-bus/bus"
)

func scopeTokenDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// auditScopeTokenChild runs inside a managed child. It reports only presence,
// never a value: no privileged variable is set, and no environment entry
// contains the scope token, which the parent names by its digest alone.
func auditScopeTokenChild(t *testing.T) {
	for _, name := range []string{"OCTOBER_BUS_ADMIN_TOKEN", "OCTOBER_BUS_SCOPE_TOKEN"} {
		if os.Getenv(name) != "" {
			t.Errorf("managed child inherited privileged variable %s", name)
		}
	}
	digest, size := os.Getenv("BUS_AUDIT_SCOPE_DIGEST"), base64.RawURLEncoding.EncodedLen(32)
	for _, entry := range os.Environ() {
		for start := 0; start+size <= len(entry); start++ {
			if scopeTokenDigest(entry[start:start+size]) == digest {
				name, _, _ := strings.Cut(entry, "=")
				t.Errorf("managed child variable %s contains the scope token", name)
			}
		}
	}
}

func TestScopeTokenChild(t *testing.T) {
	if os.Getenv("BUS_AUDIT_CHILD") != "1" {
		return
	}
	auditScopeTokenChild(t)
}

func TestReadScopeTokenFileRequiresPrivateRegularFile(t *testing.T) {
	dir := t.TempDir()
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x5a}, 32))
	path := filepath.Join(dir, "scope.token")
	requireNoError(t, os.WriteFile(path, []byte(token+"\n"), 0o600))
	got, err := readScopeTokenFile(path)
	require(t, err == nil && got == token, "private scope token file refused: %v", err)

	write := func(name string, data []byte, mode os.FileMode) string {
		file := filepath.Join(dir, name)
		requireNoError(t, os.WriteFile(file, data, mode))
		requireNoError(t, os.Chmod(file, mode))
		return file
	}
	refused := map[string]string{
		"empty":            write("empty", nil, 0o600),
		"malformed":        write("malformed", []byte("not-a-scope-token\n"), 0o600),
		"line break":       write("split", []byte(token[:20]+"\n"+token[20:]+"\n"), 0o600),
		"oversized":        write("oversized", bytes.Repeat([]byte("A"), maxScopeTokenBytes+1), 0o600),
		"missing":          filepath.Join(dir, "missing"),
		"not regular file": dir,
	}
	if runtime.GOOS != "windows" {
		refused["group readable"] = write("group", []byte(token), 0o640)
		refused["world readable"] = write("world", []byte(token), 0o644)
		link := filepath.Join(dir, "link")
		requireNoError(t, os.Symlink(path, link))
		refused["symlink"] = link
	}
	for name, file := range refused {
		_, err := readScopeTokenFile(file)
		require(t, err != nil, "%s scope token file accepted", name)
		require(t, !strings.Contains(err.Error(), token), "%s: error revealed the scope token", name)
	}
}

func TestAgentRunReadsScopeTokenFromPrivateFile(t *testing.T) {
	ctx := context.Background()
	runtimeValue, err := bus.Open(":memory:")
	requireNoError(t, err)
	server := bus.NewServer(runtimeValue, bus.ServerOptions{})
	address, err := server.Start()
	requireNoError(t, err)
	defer server.Stop(ctx)
	scope, err := runtimeValue.CreateScope(ctx, bus.CreateScopeInput{ID: "token-file"})
	requireNoError(t, err)
	path := filepath.Join(t.TempDir(), "scope.token")
	requireNoError(t, os.WriteFile(path, []byte(scope.ScopeToken+"\n"), 0o600))
	t.Setenv("OCTOBER_BUS_SCOPE_TOKEN", "")
	t.Setenv("OCTOBER_BUS_ADMIN_TOKEN", "synthetic-admin-marker")
	t.Setenv("BUS_AUDIT_CHILD", "1")
	t.Setenv("BUS_AUDIT_SCOPE_DIGEST", scopeTokenDigest(scope.ScopeToken))
	args := func(extra ...string) []string {
		return append(append([]string{"--id", "worker", "--name", "Worker", "--address", address}, extra...),
			"--", os.Args[0], "-test.run=^TestScopeTokenChild$")
	}
	requireNoError(t, runAgent(args("--scope-token-file", path)))
	agents, err := (bus.Client{Address: address, Token: scope.ScopeToken}).ListAgents(ctx)
	require(t, err == nil && len(agents) == 1 && agents[0].ID == "worker" && agents[0].Lifecycle == bus.LifecycleOffline,
		"file-launched agent did not register and retire: %#v, %v", agents, err)

	err = runAgent(args("--scope-token-file", path, "--scope", "token-file"))
	require(t, err != nil && strings.Contains(err.Error(), "use only one of"), "accepted two scope token sources: %v", err)
	t.Setenv("OCTOBER_BUS_SCOPE_TOKEN", scope.ScopeToken)
	err = runAgent(args("--scope-token-file", path))
	require(t, err != nil && strings.Contains(err.Error(), "unset OCTOBER_BUS_SCOPE_TOKEN"), "accepted a file token beside an environment token: %v", err)
	t.Setenv("OCTOBER_BUS_SCOPE_TOKEN", "")
	if runtime.GOOS != "windows" {
		requireNoError(t, os.Chmod(path, 0o644))
		err = runAgent(args("--scope-token-file", path))
		require(t, err != nil && strings.Contains(err.Error(), "owner-only"), "accepted a shared scope token file: %v", err)
	}
}
