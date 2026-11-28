package bus

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLocalScopeCredentials(t *testing.T) {
	dir := t.TempDir()
	token := strings.Repeat("a", 43)
	for _, id := range []string{"scope", "Scope", "CON", "scope:branch"} {
		if err := SaveScopeToken(dir, id, token); err != nil {
			t.Fatal(err)
		}
		got, err := ReadScopeToken(dir, id)
		if err != nil || got != token {
			t.Fatalf("round trip %s: %v", id, err)
		}
		path, _ := ScopeTokenPath(dir, id)
		if filepath.Dir(path) != filepath.Join(dir, "scopes") || strings.Contains(filepath.Base(path), ":") {
			t.Fatal("unsafe filename")
		}
	}
	for _, id := range []string{"", "../escape", "/absolute", `..\escape`} {
		if err := SaveScopeToken(dir, id, token); err == nil {
			t.Fatalf("accepted invalid ID %q", id)
		}
	}
	if err := SaveScopeToken(dir, "scope", "secret-looking-invalid-token"); err == nil {
		t.Fatal("accepted malformed token")
	}
	path, _ := ScopeTokenPath(dir, "scope")
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadScopeToken(dir, "scope"); err == nil {
			t.Fatal("read shared credential")
		}
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := SaveScopeToken(dir, "scope", strings.Repeat("b", 43)); err != nil {
		t.Fatal(err)
	}
	if err := RemoveScopeToken(dir, "scope"); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadScopeToken(dir, "scope"); !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), "agentintercom workspace create scope") {
		t.Fatalf("missing credential error = %v", err)
	}
	if _, err := ReadScopeToken(filepath.Join(dir, "never-created"), "scope"); !errors.Is(err, fs.ErrNotExist) || strings.Contains(err.Error(), ".token") {
		t.Fatalf("missing data directory error = %v", err)
	}
	if err := RemoveScopeToken(dir, "scope"); err != nil {
		t.Fatal(err)
	}
}

func TestLocalScopeCredentialMutationLock(t *testing.T) {
	dir := t.TempDir()
	first, err := LockScopeCredentials(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if other, err := LockScopeCredentials(dir); err == nil {
		other.Close()
		t.Fatal("concurrent credential mutation acquired the same lock")
	}
	first.Close()
	second, err := LockScopeCredentials(dir)
	if err != nil {
		t.Fatal(err)
	}
	second.Close()
}

func TestLocalScopeCredentialsRejectSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires Windows privileges")
	}
	dir := t.TempDir()
	if err := SaveScopeToken(dir, "scope", strings.Repeat("a", 43)); err != nil {
		t.Fatal(err)
	}
	path, _ := ScopeTokenPath(dir, "scope")
	if err := os.Rename(path, path+".real"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path+".real", path); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadScopeToken(dir, "scope"); err == nil {
		t.Fatal("read symlink credential")
	}
	if err := SaveScopeToken(dir, "scope", strings.Repeat("b", 43)); err == nil {
		t.Fatal("overwrote symlink credential")
	}
}
