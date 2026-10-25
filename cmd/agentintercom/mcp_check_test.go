package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMCPCheckProvesAdmissionWithoutInitializingOrConsuming(t *testing.T) {
	path, connection := connectionFixture(t)
	connection.HookToken = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))
	upstream := newFakeUpstream(t, connection.AgentToken)
	connection.Endpoint = upstream.server.URL + "/mcp"
	saveTestConnection(t, path, connection)
	report := checkManagedConnection(context.Background(), path)
	require(t, report.Status == "ok" && report.HookCredential && report.ScopeID == connection.ScopeID && report.ExecutionID == connection.ExecutionID, "check report: %#v; %v", report, upstream.lastFailures())
	require(t, upstream.initializes.Load() == 0 && upstream.calls.Load() == 0, "check initialized MCP or called a tool: %d %d", upstream.initializes.Load(), upstream.calls.Load())

	t.Setenv(managedConnectionFileEnv, path)
	var out bytes.Buffer
	requireNoError(t, runMCPCheck([]string{"--json"}, &out))
	var decoded connectionCheck
	requireNoError(t, json.Unmarshal(out.Bytes(), &decoded))
	require(t, decoded.Status == "ok" && decoded.RuntimeVersion != "" && decoded.ProtocolVersion != "", "json report: %s", out.String())
	require(t, !strings.Contains(out.String(), connection.AgentToken) && !strings.Contains(out.String(), connection.HookToken), "report leaked a credential")

	upstream.refuse.Store(true)
	out.Reset()
	err := runMCPCheck([]string{"--connection-file", path}, &out)
	require(t, err != nil && strings.Contains(out.String(), "Managed connection: refused-credential"), "refusal not reported: %v %s", err, out.String())
}

func TestMCPCheckClassifiesFailures(t *testing.T) {
	envelope := func(code string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"apiVersion":1,"ok":false,"error":{"code":"` + code + `","message":"masked"}}`))
		}
	}
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		mutate  func(*managedMCPConnection)
		status  string
	}{
		{"core-not-ready", envelope("CORE_NOT_READY"), nil, "authority-unavailable"},
		{"core-refused", envelope("UNAUTHENTICATED"), nil, "refused-credential"},
		{"core-retired", envelope("CANCELLED"), nil, "retired-execution"},
		{"gateway-forbidden", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "forbidden", http.StatusForbidden) }, nil, "refused-credential"},
		{"gateway-down", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", http.StatusBadGateway) }, nil, "authority-unavailable"},
		{"mcp-handler-bad-request", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "Bad Request: missing session", http.StatusBadRequest)
		}, nil, "protocol"},
		{"cloud-credential-refusal", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"The worker capability is invalid."}`))
		}, nil, "protocol"},
		{"html-success", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("<html>not an MCP endpoint</html>"))
		}, nil, "protocol"},
		{"rpc-error-success-status", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"masked"}}`))
		}, nil, "protocol"},
		{"mismatched-ping-id", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{}}`))
		}, nil, "protocol"},
		{"redirect", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://elsewhere.example/mcp", http.StatusFound)
		}, nil, "protocol"},
		{"unreachable", nil, func(c *managedMCPConnection) { c.Endpoint = "http://127.0.0.1:1/mcp" }, "unreachable"},
		{"expired", nil, func(c *managedMCPConnection) { c.ExpiresAt = time.Now().Add(-time.Hour) }, "expired-credential"},
		{"malformed", nil, func(c *managedMCPConnection) { c.AgentToken = "nope" }, "invalid-configuration"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, connection := connectionFixture(t)
			if tc.handler != nil {
				server := httptest.NewServer(tc.handler)
				defer server.Close()
				connection.Endpoint = server.URL + "/mcp"
			}
			if tc.mutate != nil {
				tc.mutate(&connection)
			}
			saveTestConnection(t, path, connection)
			report := checkManagedConnection(context.Background(), path)
			require(t, report.Status == tc.status, "expected %s, got %#v", tc.status, report)
			require(t, !strings.Contains(report.Problem, "masked"), "authority body leaked into the report: %#v", report)
		})
	}
	missing := checkManagedConnection(context.Background(), "")
	require(t, missing.Status == "invalid-configuration" && strings.Contains(missing.Problem, managedConnectionFileEnv), "missing path: %#v", missing)
	absent := checkManagedConnection(context.Background(), t.TempDir()+"/absent.json")
	require(t, absent.Status == "invalid-configuration", "absent file: %#v", absent)
}

func TestVersionJSON(t *testing.T) {
	var out bytes.Buffer
	requireNoError(t, printVersion([]string{"--json"}, &out))
	var decoded map[string]string
	requireNoError(t, json.Unmarshal(out.Bytes(), &decoded))
	require(t, decoded["runtimeVersion"] != "" && decoded["protocolVersion"] != "", "version json: %s", out.String())
	out.Reset()
	requireNoError(t, printVersion(nil, &out))
	require(t, strings.HasPrefix(out.String(), "agentintercom "), "human version: %s", out.String())
	require(t, printVersion([]string{"extra"}, &out) != nil, "positional argument accepted")
}

func removeTestConnection(path string) error { return os.Remove(path) }
