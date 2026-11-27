//go:build darwin || linux

package bus

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadRunFileRefusesFilesOtherUsersCanPlant(t *testing.T) {
	write := func(t *testing.T, directoryMode, fileMode os.FileMode) string {
		t.Helper()
		directory := filepath.Join(t.TempDir(), "run")
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(directory, "bus.json")
		run := RunFile{ProtocolVersion: ProtocolVersion, Address: "http://127.0.0.1:1", PID: os.Getpid(), StartedAt: time.Now().UTC().Format(time.RFC3339Nano), AdminToken: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
		if err := writeRunFile(path, run); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, fileMode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(directory, directoryMode); err != nil {
			t.Fatal(err)
		}
		return path
	}
	if _, err := ReadRunFile(write(t, 0o700, 0o600)); err != nil {
		t.Fatalf("private run file was refused: %v", err)
	}
	for name, modes := range map[string][2]os.FileMode{
		"group-readable file":      {0o700, 0o640},
		"world-writable directory": {0o777, 0o600},
	} {
		if _, err := ReadRunFile(write(t, modes[0], modes[1])); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
	target := write(t, 0o700, 0o600)
	link := filepath.Join(filepath.Dir(target), "link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRunFile(link); err == nil {
		t.Fatal("symlinked run file was accepted")
	}
	if os.Geteuid() == 0 {
		planted := write(t, 0o700, 0o600)
		if err := os.Chown(planted, 65534, 65534); err != nil {
			t.Skip("cannot change file ownership:", err)
		}
		if _, err := ReadRunFile(planted); err == nil {
			t.Fatal("run file owned by another user was accepted")
		}
		if err := os.Chown(filepath.Dir(planted), 65534, 65534); err != nil {
			t.Fatal(err)
		}
		if err := secureDirectory(filepath.Dir(planted)); err == nil {
			t.Fatal("runtime directory owned by another user was accepted")
		}
	}
}
