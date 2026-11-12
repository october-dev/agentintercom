package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/october-dev/agentintercom/bus"
)

func TestManagedInstallPreservesConfiguration(t *testing.T) {
	original := []byte("# My settings\nmodel = \"custom\"\n\n[mcp_servers.other]\ncommand = \"other\"\n")
	after, err := managedTOML(original, []byte("[mcp_servers.agent_intercom]\ncommand = \"agentintercom\"\n"))
	requireNoError(t, err)
	r := installedConfig{Path: "config.toml", Before: original, After: after}
	removed, err := removeInstalledConfig(r, after)
	requireNoError(t, err)
	require(t, bytes.Equal(original, removed), "did not restore exact original bytes")
	edited := append(append([]byte(nil), after...), []byte("\n[features]\nexample = true\n")...)
	removed, err = removeInstalledConfig(r, edited)
	requireNoError(t, err)
	require(t, bytes.Equal(removed, append(append([]byte(nil), original...), []byte("\n[features]\nexample = true\n")...)), "unrelated TOML edits lost")
	_, err = removeInstalledConfig(r, bytes.ReplaceAll(after, []byte("command = \"agentintercom\""), []byte("command = \"my-wrapper\"")))
	require(t, err != nil, "removed an edited owned block")
	for _, existing := range []string{"[mcp_servers.agent_intercom]\n", "[mcp_servers.\"agent_intercom\"]\n", "[ mcp_servers . 'agent_intercom' . env ]\n", managedBegin} {
		_, err := managedTOML([]byte(existing), []byte("new"))
		require(t, err != nil, "overwrote unmanaged entry: %s", existing)
	}
}

func TestClaudeUninstallPreservesUnrelatedUpdates(t *testing.T) {
	before := []byte(`{"theme":"dark","mcpServers":{"other":{"command":"other"}}}`)
	after := []byte(`{"theme":"dark","mcpServers":{"other":{"command":"other"},"agent_intercom":{"command":"agentintercom"}}}`)
	current := []byte(`{"theme":"light","onboarding":true,"mcpServers":{"other":{"command":"changed"},"agent_intercom":{"command":"agentintercom"}}}`)
	got, err := removeInstalledConfig(installedConfig{".claude.json", before, after}, current)
	requireNoError(t, err)
	var value map[string]any
	requireNoError(t, json.Unmarshal(got, &value))
	require(t, value["theme"] == "light" && value["onboarding"] == true, "lost unrelated app settings")
	servers := value["mcpServers"].(map[string]any)
	require(t, len(servers) == 1 && servers["other"].(map[string]any)["command"] == "changed", "lost another MCP entry")
}

func TestClaudeHooksRestoreAndRejectChanges(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("Unix command hooks")
	}
	root := t.TempDir()
	paths := bus.DaemonPaths{DataDir: filepath.Join(root, "data"), RuntimeDir: filepath.Join(root, "run")}
	before := []byte(`{"theme":"dark","hooks":{"Stop":[{"hooks":[{"type":"command","command":"echo keep"}]}]}}`)
	after, err := claudeHooks(before, filepath.Join(root, "bin with space"), paths)
	requireNoError(t, err)
	var settings map[string]any
	requireNoError(t, json.Unmarshal(after, &settings))
	settings["theme"] = "light"
	current, _ := json.Marshal(settings)
	got, err := removeInstalledConfig(installedConfig{"settings.json", before, after}, current)
	requireNoError(t, err)
	requireNoError(t, json.Unmarshal(got, &settings))
	require(t, settings["theme"] == "light", "lost changed theme")
	hooks := settings["hooks"].(map[string]any)
	require(t, len(hooks) == 1 && len(hooks["Stop"].([]any)) == 1, "removed unrelated hook or retained managed ones")
}

func TestServicePathsAndConfigSymlinks(t *testing.T) {
	root := t.TempDir()
	paths := bus.DaemonPaths{DataDir: filepath.Join(root, "data with space"), RuntimeDir: filepath.Join(root, "run")}
	_, linux, err := serviceFile("linux", root, filepath.Join(root, "bin", "agentintercom"), paths)
	requireNoError(t, err)
	require(t, strings.Contains(string(linux), "Restart=on-failure") && strings.Contains(string(linux), "OCTOBER_BUS_RUNTIME_DIR="), "missing lifetime/runtime settings")
	_, mac, err := serviceFile("darwin", root, filepath.Join(root, "a&b"), paths)
	requireNoError(t, err)
	require(t, strings.Contains(string(mac), "a&amp;b"), "plist path was not escaped")
	if os.PathSeparator == '\\' {
		return
	}
	dest := filepath.Join(root, "config")
	requireNoError(t, os.WriteFile(dest, []byte("keep"), 0600))
	link := filepath.Join(root, "link")
	requireNoError(t, os.Symlink(dest, link))
	require(t, writeConfigFile(link, []byte("overwrite")) != nil, "followed a config symlink")
	got, err := os.ReadFile(dest)
	requireNoError(t, err)
	require(t, string(got) == "keep", "changed symlink target")
}
