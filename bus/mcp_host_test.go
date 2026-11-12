package bus

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestMCPHostAllowed(t *testing.T) {
	tests := []struct {
		name    string
		host    string
		allowed []string
		want    bool
	}{
		{name: "IPv4 with port", host: "127.0.0.1:4765", want: true},
		{name: "IPv4 without port", host: "127.0.0.1", want: true},
		{name: "IPv4 loopback range", host: "127.5.5.5:1", want: true},
		{name: "localhost with port", host: "localhost:4765", want: true},
		{name: "localhost without port", host: "localhost", want: true},
		{name: "localhost case insensitive", host: "LOCALHOST:4765", want: true},
		{name: "IPv6 with port", host: "[::1]:4765", want: true},
		{name: "IPv6 without brackets", host: "::1", want: true},
		{name: "IPv6 with brackets", host: "[::1]", want: true},
		{name: "bracketed IPv4", host: "[127.0.0.1]", want: true},
		{name: "exact allowlist entry", host: "192.168.1.20:4765", allowed: []string{"192.168.1.20:4765"}, want: true},
		{name: "empty Host", host: "", want: false},
		{name: "empty Host cannot be allowlisted", host: "", allowed: []string{""}, want: false},
		{name: "remote IP", host: "192.168.1.20:4765", want: false},
		{name: "allowlist port differs", host: "192.168.1.20:4766", allowed: []string{"192.168.1.20:4765"}, want: false},
		{name: "allowlist includes port", host: "192.168.1.20", allowed: []string{"192.168.1.20:4765"}, want: false},
		{name: "allowlist is case sensitive", host: "Bus.Internal:4765", allowed: []string{"bus.internal:4765"}, want: false},
		{name: "remote name", host: "example.com", want: false},
		{name: "loopback suffix attack", host: "127.0.0.1.evil.example:4765", want: false},
		{name: "nested brackets", host: "[[::1]]", want: false},
		{name: "unmatched opening bracket", host: "[127.0.0.1", want: false},
		{name: "unmatched closing bracket", host: "localhost]", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := &Server{options: ServerOptions{AllowedHosts: test.allowed}}
			if got := server.mcpHostAllowed(test.host); got != test.want {
				t.Fatalf("mcpHostAllowed(%q) = %t, want %t", test.host, got, test.want)
			}
		})
	}
}

func TestServeMCPHostPolicy(t *testing.T) {
	agents := setupAgents(t, ":memory:")
	defer agents.runtime.Close()

	request := func(server *Server, host string, authorized bool) *httptest.ResponseRecorder {
		httpRequest := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		httpRequest.Host = host
		httpRequest.Header.Set("Content-Type", "application/json")
		httpRequest.Header.Set("Accept", "application/json, text/event-stream")
		if authorized {
			httpRequest.Header.Set("Authorization", "Bearer "+agents.plannerToken)
		}
		response := httptest.NewRecorder()
		server.ServeHTTP(response, httpRequest)
		return response
	}

	assertFailure := func(response *httptest.ResponseRecorder, status int, code ErrorCode) {
		t.Helper()
		var payload struct {
			OK    bool `json:"ok"`
			Error struct {
				Code ErrorCode `json:"code"`
			} `json:"error"`
		}
		requireNoError(t, json.Unmarshal(response.Body.Bytes(), &payload))
		if response.Code != status || payload.OK || payload.Error.Code != code {
			t.Fatalf("unexpected failure: status=%d payload=%#v", response.Code, payload)
		}
	}

	server := NewServer(agents.runtime, ServerOptions{})
	assertFailure(request(server, "192.168.1.20:4765", true), http.StatusForbidden, CodePermissionDenied)

	response := request(server, "127.0.0.1:4765", true)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "list_peers") {
		t.Fatalf("loopback MCP request failed: status=%d body=%s", response.Code, response.Body.String())
	}

	server = NewServer(agents.runtime, ServerOptions{AllowedHosts: []string{"192.168.1.20:4765"}})
	response = request(server, "192.168.1.20:4765", true)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "list_peers") {
		t.Fatalf("allowlisted MCP request failed: status=%d body=%s", response.Code, response.Body.String())
	}
	assertFailure(request(server, "192.168.1.20:4766", true), http.StatusForbidden, CodePermissionDenied)

	server = NewServer(agents.runtime, ServerOptions{})
	assertFailure(request(server, "192.168.1.20:4765", false), http.StatusUnauthorized, CodeUnauthenticated)
}

func TestMCPAddTaskSchemaIncludesTitle(t *testing.T) {
	agents := setupAgents(t, ":memory:")
	defer agents.runtime.Close()
	server := NewServer(agents.runtime, ServerOptions{})
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	request.Host = "127.0.0.1:4765"
	request.Header.Set("Authorization", "Bearer "+agents.plannerToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("tools/list failed: status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				InputSchema struct {
					Properties map[string]any `json:"properties"`
					Required   []string       `json:"required"`
				} `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	for _, tool := range payload.Result.Tools {
		if tool.Name != "add_task" {
			continue
		}
		if _, ok := tool.InputSchema.Properties["title"]; !ok {
			t.Fatalf("add_task schema omitted title: %s", response.Body.String())
		}
		for _, required := range tool.InputSchema.Required {
			if required == "title" {
				return
			}
		}
		t.Fatalf("add_task schema did not require title: %s", response.Body.String())
	}
	t.Fatal("add_task tool was not listed")
}

// Some MCP hosts (Cursor 3.20.21) reject a tool list that contains a boolean
// property schema such as `"value": true`, which hides every tool in the server.
func TestMCPToolSchemasAvoidBooleanPropertySchemas(t *testing.T) {
	agents := setupAgents(t, ":memory:")
	defer agents.runtime.Close()
	server := NewServer(agents.runtime, ServerOptions{})
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	request.Host = "127.0.0.1:4765"
	request.Header.Set("Authorization", "Bearer "+agents.plannerToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("tools/list failed: status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				InputSchema struct {
					Properties map[string]any `json:"properties"`
				} `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Result.Tools) == 0 {
		t.Fatal("no tools were listed")
	}
	for _, tool := range payload.Result.Tools {
		for name, schema := range tool.InputSchema.Properties {
			if _, isObject := schema.(map[string]any); !isObject {
				t.Errorf("%s property %q has a non-object schema %v; hosts such as Cursor reject it", tool.Name, name, schema)
			}
		}
	}
}

func TestDaemonAllowedHostsEnvironment(t *testing.T) {
	t.Setenv("OCTOBER_BUS_ALLOWED_HOSTS", " a:1 , , b:2 ,a:1 ")
	root := t.TempDir()
	paths := DaemonPaths{
		DataDir: filepath.Join(root, "data"), RuntimeDir: filepath.Join(root, "run"),
		Database: filepath.Join(root, "data", "bus.db"), RunFile: filepath.Join(root, "run", "bus.json"),
		LockFile: filepath.Join(root, "run", "bus.lock"),
	}
	daemon, err := StartDaemon(context.Background(), 0, &paths)
	requireNoError(t, err)
	defer daemon.Stop(context.Background())
	want := []string{"a:1", "b:2"}
	if !reflect.DeepEqual(daemon.Server.options.AllowedHosts, want) {
		t.Fatalf("allowed Hosts = %#v, want %#v", daemon.Server.options.AllowedHosts, want)
	}
}
