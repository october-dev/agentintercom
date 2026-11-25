package adapters

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"
)

func TestCursorManagedConfigurationForwardsLauncherIdentity(t *testing.T) {
	data, err := files.ReadFile("cursor/mcp.agent-run.json.example")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Servers map[string]struct {
			Type    string
			Command string
			Args    []string
			Env     map[string]string
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	server := config.Servers["agent_intercom"]
	if len(config.Servers) != 1 || server.Type != "stdio" || server.Command != "agentintercom" || !slices.Equal(server.Args, []string{"mcp", "stdio"}) {
		t.Fatal("expected one stdio bridge using the launcher's execution")
	}
	want := map[string]string{
		"OCTOBER_BUS_ADDRESS":     "${env:OCTOBER_BUS_ADDRESS}",
		"OCTOBER_BUS_AGENT_TOKEN": "${env:OCTOBER_BUS_AGENT_TOKEN}",
	}
	if !maps.Equal(server.Env, want) {
		t.Fatal("expected only references to the execution endpoint and credential")
	}
}

func TestEveryConfigurationRendersWithoutCredentials(t *testing.T) {
	all, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) < 28 {
		t.Fatal("expected the reviewed MCP host configurations")
	}
	values := map[string]string{"command": `C:\Bus With Spaces\agentintercom.exe`, "scope": "project", "agent": "editor", "name": `Reviewer "One"`, "dataDir": `C:\User Data\bus`, "runtimeDir": `C:\User Data\run`}
	for _, manifest := range all {
		if manifest.Configuration == nil {
			continue
		}
		t.Run(manifest.Directory, func(t *testing.T) {
			data, err := Render(manifest, values)
			if err != nil {
				t.Fatal(err)
			}
			for _, required := range []string{"--scope", "--agent", "--data-dir", "--runtime-dir"} {
				if !strings.Contains(string(data), required) {
					t.Fatalf("missing %s", required)
				}
			}
			if strings.Contains(string(data), "TOKEN") || strings.Contains(string(data), "{{") {
				t.Fatal("credential or unresolved placeholder")
			}
			if manifest.Configuration.Format != "toml" && !json.Valid(data) {
				t.Fatal("JSON/YAML-subset snippet must parse")
			}
		})
	}
	manifest, _ := Lookup("codex")
	values["name"] = "$(touch should-not-run)"
	if _, err := Render(manifest, values); err == nil {
		t.Fatal("accepted host-interpreted shell expansion")
	}
	if _, err := Lookup("../../other"); err == nil {
		t.Fatal("accepted unknown host path")
	}
	if _, err := Lookup("october-harness"); err == nil {
		t.Fatal("native launcher must not return a fabricated MCP configuration")
	}
}
