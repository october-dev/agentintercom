package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// managedConnectionFileEnv names the connection file for harness processes whose
// configuration is shared and cannot carry a per-execution path. A managed
// launcher exports it into the execution's environment; it holds a path, never
// a credential.
const managedConnectionFileEnv = "OCTOBER_BUS_CONNECTION_FILE"

// connectionFailure classifies why a managed connection could not be used. The
// bridge, hook and check commands print only the class and a fixed sentence.
// Credentials, request bodies and authority responses never appear in them.
type connectionFailure string

const (
	failureConfiguration connectionFailure = "invalid-configuration"
	failureExpired       connectionFailure = "expired-credential"
	failureRetired       connectionFailure = "retired-execution"
	failureUnreachable   connectionFailure = "unreachable"
	failureRefused       connectionFailure = "refused-credential"
	failureUnavailable   connectionFailure = "authority-unavailable"
	failureSession       connectionFailure = "session-lost"
	failureProtocol      connectionFailure = "protocol"
)

type connectionError struct {
	Class connectionFailure
	// Missing is set when the file does not exist. The first read treats that as
	// configuration; a renewal read treats it as withdrawal by the controller.
	Missing bool
	Reason  string
}

func (err *connectionError) Error() string { return string(err.Class) + ": " + err.Reason }

// A host controller owns this file and renews it by atomic rename. The bridge
// receives only execution credentials, never scope or administrator authority.
// These labels pin the connection; the authority still authenticates every call.
type managedMCPConnection struct {
	Version  int    `json:"version"`
	Endpoint string `json:"endpoint"`
	// Gateway binds cloud connections to their controller's HTTPS origin. It
	// does not select another endpoint or transport; requests use Endpoint.
	Gateway     string `json:"gateway,omitempty"`
	HTTPHost    string `json:"httpHost,omitempty"`
	ScopeID     string `json:"scopeId"`
	AgentID     string `json:"agentId"`
	ExecutionID string `json:"executionId"`
	AgentToken  string `json:"agentToken"`
	// HookToken is the optional per-execution lifecycle credential used by
	// `agentintercom hook`. It is issued and revoked with this execution; it is not
	// a controller administrator credential or a process-lifetime hook secret.
	HookToken string    `json:"hookToken,omitempty"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// resolveManagedConnectionPath prefers an explicit flag over the environment
// variable that a managed launcher exports into the harness process.
func resolveManagedConnectionPath(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	return strings.TrimSpace(os.Getenv(managedConnectionFileEnv))
}

// validExecutionToken accepts the canonical unpadded base64url form of a
// 32-byte credential, the shape every supported authority mints.
func validExecutionToken(value string) bool {
	token, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(token) == 32 && base64.RawURLEncoding.EncodeToString(token) == value
}

func readManagedMCPConnection(path string) (managedMCPConnection, error) {
	var connection managedMCPConnection
	malformed := &connectionError{Class: failureConfiguration, Reason: "managed connection file is malformed, shared, or names an unsupported endpoint; reconnect through its host controller"}
	// Remote workers currently run on Unix. Do not pretend POSIX bits check a
	// Windows ACL; the existing environment/local-discovery modes remain available.
	if runtime.GOOS == "windows" || !filepath.IsAbs(path) {
		return connection, &connectionError{Class: failureConfiguration, Reason: "managed MCP connections require an absolute path with Unix owner-only permissions"}
	}
	parent, err := os.Lstat(filepath.Dir(path))
	if err != nil || !parent.IsDir() || parent.Mode().Perm()&0o077 != 0 {
		return connection, &connectionError{Class: failureConfiguration, Reason: "managed connection directory is missing, shared, or not a real directory"}
	}
	before, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return connection, &connectionError{Class: failureConfiguration, Missing: true, Reason: "managed connection file is missing"}
	}
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm()&0o077 != 0 || before.Size() > 16*1024 {
		return connection, malformed
	}
	file, err := os.Open(path)
	if err != nil {
		return connection, malformed
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || !after.Mode().IsRegular() || after.Mode().Perm()&0o077 != 0 {
		return connection, malformed
	}
	data, err := io.ReadAll(io.LimitReader(file, 16*1024+1))
	if err != nil || len(data) > 16*1024 {
		return connection, malformed
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&connection) != nil || decoder.Decode(new(any)) != io.EOF {
		return managedMCPConnection{}, malformed
	}
	if connection.Version != 1 {
		return managedMCPConnection{}, malformed
	}
	for _, id := range []string{connection.ScopeID, connection.AgentID, connection.ExecutionID} {
		if id == "" || len(id) > 256 || strings.ContainsAny(id, "\x00\r\n") {
			return managedMCPConnection{}, malformed
		}
	}
	if !validExecutionToken(connection.AgentToken) || (connection.HookToken != "" && !validExecutionToken(connection.HookToken)) {
		return managedMCPConnection{}, malformed
	}
	endpoint, err := url.Parse(connection.Endpoint)
	if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return managedMCPConnection{}, malformed
	}
	loopback := net.ParseIP(endpoint.Hostname())
	if endpoint.Scheme != "https" && !(endpoint.Scheme == "http" && loopback != nil && loopback.IsLoopback()) {
		return managedMCPConnection{}, malformed
	}
	if connection.Gateway != "" {
		gateway, err := url.Parse(connection.Gateway)
		if err != nil || gateway.Scheme != "https" || gateway.Host == "" || gateway.User != nil || gateway.RawQuery != "" || gateway.ForceQuery || gateway.Fragment != "" || (gateway.Path != "" && gateway.Path != "/") || gateway.RawPath != "" || endpoint.Scheme != gateway.Scheme || endpoint.Host != gateway.Host {
			return managedMCPConnection{}, malformed
		}
	}
	if connection.HTTPHost != "" {
		host, port, err := net.SplitHostPort(connection.HTTPHost)
		portNumber, portErr := strconv.Atoi(port)
		// A TCP tunnel preserves HTTP Host. Permit only its loopback authority
		// port to differ; no arbitrary virtual-host or HTTPS routing override.
		if err != nil || portErr != nil || portNumber < 1 || portNumber > 65535 || endpoint.Scheme != "http" || host != endpoint.Hostname() {
			return managedMCPConnection{}, malformed
		}
	}
	// Expiry is checked last so an expired but otherwise valid file reports the
	// renewable condition rather than a configuration defect.
	if connection.ExpiresAt.IsZero() {
		return managedMCPConnection{}, malformed
	}
	if !connection.ExpiresAt.After(time.Now()) {
		return managedMCPConnection{}, &connectionError{Class: failureExpired, Reason: "managed connection credential expired; the host controller must renew the file"}
	}
	return connection, nil
}

func managedMCPConnectionSource(path string, original managedMCPConnection) func() (managedMCPConnection, error) {
	return func() (managedMCPConnection, error) {
		current, err := readManagedMCPConnection(path)
		var failure *connectionError
		if errors.As(err, &failure) && failure.Missing {
			return managedMCPConnection{}, &connectionError{Class: failureRetired, Missing: true, Reason: "managed connection file was withdrawn by its host controller"}
		}
		if err != nil {
			return managedMCPConnection{}, err
		}
		if current.Endpoint != original.Endpoint || current.Gateway != original.Gateway || current.ScopeID != original.ScopeID || current.AgentID != original.AgentID || current.ExecutionID != original.ExecutionID {
			return managedMCPConnection{}, &connectionError{Class: failureRetired, Reason: "managed MCP execution changed; start a new bridge through its host controller"}
		}
		return current, nil
	}
}

func refuseMCPRedirect(_ *http.Request, _ []*http.Request) error {
	return &connectionError{Class: failureProtocol, Reason: "MCP endpoint redirected; reconnect through its host controller"}
}

// hookBase derives the controller's lifecycle routes from the MCP endpoint.
// Desktop Core serves /mcp and /hook/* on one origin and path prefix.
func hookBase(endpoint string) (string, bool) {
	base, ok := strings.CutSuffix(endpoint, "/mcp")
	return base, ok
}

// classifyTransportError maps a failed HTTP exchange to a failure class. Errors
// raised by the connection file pass through with their own class.
func classifyTransportError(err error) (connectionFailure, string) {
	var failure *connectionError
	if errors.As(err, &failure) {
		return failure.Class, failure.Reason
	}
	return failureUnreachable, "the MCP endpoint could not be reached"
}

const failureBodyLimit = 64 * 1024

// classifyHTTPFailure maps a non-2xx response to a failure class. Desktop Core
// answers refusals with HTTP 400 and a JSON envelope whose error code is
// authoritative; the standalone Bus and its gateway use HTTP statuses.
func classifyHTTPFailure(status int, body []byte) (connectionFailure, string) {
	var envelope struct {
		Error struct {
			Code json.RawMessage `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &envelope) == nil && len(envelope.Error.Code) > 0 {
		var code string
		if json.Unmarshal(envelope.Error.Code, &code) == nil {
			switch code {
			case "UNAUTHENTICATED", "PERMISSION_DENIED":
				return failureRefused, "the authority refused this execution credential"
			case "CORE_NOT_READY", "BACKPRESSURE", "DEADLINE_EXCEEDED":
				return failureUnavailable, "the authority is reachable but not ready"
			case "CANCELLED":
				return failureRetired, "the authority ended this execution while a request was pending"
			}
		} else {
			var rpcCode int64
			if json.Unmarshal(envelope.Error.Code, &rpcCode) == nil {
				return failureProtocol, fmt.Sprintf("the endpoint rejected the request (JSON-RPC %d, HTTP %d)", rpcCode, status)
			}
		}
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return failureRefused, "the authority refused this execution credential"
	case status == http.StatusNotFound:
		return failureSession, "the upstream MCP session ended; the next tool call reconnects"
	case status == http.StatusTooManyRequests || status >= 500:
		return failureUnavailable, "the authority is reachable but not ready"
	}
	return failureProtocol, fmt.Sprintf("unexpected HTTP %d from the MCP endpoint", status)
}

// readFailureBody buffers a bounded copy of a failed response so it can be
// classified and still handed to the caller unchanged.
func readFailureBody(response *http.Response) []byte {
	data, _ := io.ReadAll(io.LimitReader(response.Body, failureBodyLimit))
	response.Body.Close()
	response.Body = io.NopCloser(bytes.NewReader(data))
	return data
}
