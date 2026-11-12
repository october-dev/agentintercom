package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
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
