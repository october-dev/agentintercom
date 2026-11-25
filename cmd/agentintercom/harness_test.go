package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestHarnessDiagnosticsMissingSetup(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PATH", root)
	t.Setenv("OCTOBER_BUS_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("OCTOBER_BUS_RUNTIME_DIR", filepath.Join(root, "run"))
	var output bytes.Buffer
	err := captureStdout(&output, func() error { return doctorHarness("codex", "missing-scope", true) })
	require(t, err != nil, "missing setup passed diagnostics")
	var report harnessDiagnostic
	requireNoError(t, json.Unmarshal(output.Bytes(), &report))
	require(t, !report.Healthy && len(report.Problems) == 2 && report.BridgeTools == 0, "unexpected diagnosis: %#v", report)
	require(t, harness([]string{"config", "codex", "--scope", "s", "--agent", "a", "--command", "relative-bus"}) != nil, "accepted PATH-dependent executable")
}

func TestCursorDoctorIdentifiesVersionSource(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture executables use /bin/sh")
	}
	const cli = "echo 2026.09.08-6caf4ff"
	for _, tc := range []struct{ name, editor, cli, executable, version string }{
		{"cli only", "", cli, "cursor-agent", "2026.09.08-6caf4ff"},
		{"CLI installer shim", "echo 0.0.0; exit 1", cli, "cursor-agent", "2026.09.08-6caf4ff"},
		{"editor installed", "echo 3.20.21", cli, "cursor", "3.20.21"},
		{"both unavailable", "", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("PATH", dir)
			t.Setenv("OCTOBER_BUS_DATA_DIR", filepath.Join(dir, "data"))
			t.Setenv("OCTOBER_BUS_RUNTIME_DIR", filepath.Join(dir, "run"))
			t.Setenv("OCTOBER_BUS_ADMIN_TOKEN", "test-admin")
			t.Setenv("OCTOBER_BUS_SCOPE_TOKEN", "test-scope")
			t.Setenv("OCTOBER_BUS_AGENT_TOKEN", "test-agent")
			t.Setenv("OCTOBER_BUS_DATABASE_URL", "test-database")
			for name, body := range map[string]string{"cursor": tc.editor, "cursor-agent": tc.cli} {
				if body != "" {
					script := "#!/bin/sh\n" +
						`[ "$#" = 1 ] && [ "$1" = --version ] || exit 1` + "\n" +
						`[ -z "$OCTOBER_BUS_ADMIN_TOKEN$OCTOBER_BUS_SCOPE_TOKEN$OCTOBER_BUS_AGENT_TOKEN$OCTOBER_BUS_DATABASE_URL" ] || exit 1` + "\n" + body + "\n"
					requireNoError(t, os.WriteFile(filepath.Join(dir, name), []byte(script), 0700))
				}
			}
			var output bytes.Buffer
			err := captureStdout(&output, func() error { return doctorHarness("cursor", "missing-scope", true) })
			require(t, err != nil, "missing daemon passed diagnostics")
			var report harnessDiagnostic
			requireNoError(t, json.Unmarshal(output.Bytes(), &report))
			require(t, report.Executable == tc.executable && report.Version == tc.version, "wrong version source: %#v", report)
			wantProblems := 1 // missing daemon
			if tc.executable == "" {
				wantProblems++
			}
			require(t, len(report.Problems) == wantProblems, "unexpected problems: %v", report.Problems)
		})
	}
}
