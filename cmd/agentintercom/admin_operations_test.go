package main

import (
	"path/filepath"
	"testing"
)

// Remote scope admin skips the local credential cache when no local daemon runs.
func TestScopeAdminWithoutLocalDaemonSkipsCredentialCache(t *testing.T) {
	root := t.TempDir()
	t.Setenv("OCTOBER_BUS_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("OCTOBER_BUS_RUNTIME_DIR", filepath.Join(root, "run"))
	unlock, err := lockLocalScopeCredentials("http://127.0.0.1:1")
	requireNoError(t, err)
	unlock()
	requireNoError(t, updateLocalScopeCredential("http://127.0.0.1:1", "remote-scope", ""))
}
