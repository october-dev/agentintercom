package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestCLIExitHelper(t *testing.T) {
	if os.Getenv("AGENTINTERCOM_CLI_EXIT_HELPER") != "1" {
		return
	}
	os.Args = append([]string{"agentintercom"}, os.Args[3:]...)
	main()
	os.Exit(0)
}

func TestCLIHelpExitStatus(t *testing.T) {
	for _, tt := range []struct {
		args    []string
		success bool
		want    string
	}{
		{[]string{"--help"}, true, "Usage:"},
		{[]string{"install", "--help"}, true, "Usage of install:"},
		{[]string{"version", "--help"}, true, "Usage of version:"},
		{[]string{"message", "send", "--help"}, true, "Usage of message send:"},
		{[]string{"unknown-command"}, false, "unknown command"},
		{[]string{"version", "--unknown-flag"}, false, "flag provided but not defined"},
		{[]string{"message", "send", "--to"}, false, "flag needs an argument"},
	} {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			home := t.TempDir()
			cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestCLIExitHelper$", "--"}, tt.args...)...)
			cmd.Env = []string{"AGENTINTERCOM_CLI_EXIT_HELPER=1", "HOME=" + home, "PATH="}
			output, err := cmd.CombinedOutput()
			if (err == nil) != tt.success {
				t.Fatalf("exit error = %v, want success %t; output: %s", err, tt.success, output)
			}
			if !strings.Contains(string(output), tt.want) {
				t.Fatalf("output = %q, want %q", output, tt.want)
			}
			if tt.success && strings.Contains(string(output), "flag: help requested") {
				t.Fatalf("help includes error: %s", output)
			}
			entries, err := os.ReadDir(home)
			if err != nil || len(entries) != 0 {
				t.Fatalf("home contents = %v, error = %v", entries, err)
			}
		})
	}
}
