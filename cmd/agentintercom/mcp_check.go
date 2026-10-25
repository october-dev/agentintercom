package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/october-dev/october-bus/bus"
)

// connectionCheck is the machine-readable result of `mcp check`. Status is
// "ok" or a connectionFailure. "ok" means the route answered and the authority
// accepted the execution credential; it is not agent readiness, which only the
// host controller's own process evidence can establish.
type connectionCheck struct {
	Status          string `json:"status"`
	RuntimeVersion  string `json:"runtimeVersion"`
	ProtocolVersion string `json:"protocolVersion"`
	Endpoint        string `json:"endpoint,omitempty"`
	HTTPHost        string `json:"httpHost,omitempty"`
	ScopeID         string `json:"scopeId,omitempty"`
	AgentID         string `json:"agentId,omitempty"`
	ExecutionID     string `json:"executionId,omitempty"`
	ExpiresAt       string `json:"expiresAt,omitempty"`
	HookCredential  bool   `json:"hookCredential"`
	Problem         string `json:"problem,omitempty"`
}

func runMCPCheck(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("mcp check", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	connectionFile := flags.String("connection-file", "", "host-managed private execution connection file; defaults to $"+managedConnectionFileEnv)
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("mcp check does not accept positional arguments")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	report := checkManagedConnection(ctx, resolveManagedConnectionPath(*connectionFile))
	if *jsonOutput {
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(out, string(encoded))
	} else {
		printConnectionCheck(out, report)
	}
	if report.Status != "ok" {
		return errors.New("managed connection check did not pass")
	}
	return nil
}

func checkManagedConnection(ctx context.Context, path string) connectionCheck {
	report := connectionCheck{RuntimeVersion: bus.Version, ProtocolVersion: bus.ProtocolVersion}
	if path == "" {
		report.Status = string(failureConfiguration)
		report.Problem = "pass --connection-file or set " + managedConnectionFileEnv
		return report
	}
	connection, err := readManagedMCPConnection(path)
	if err != nil {
		class, problem := classifyTransportError(err)
		report.Status, report.Problem = string(class), problem
		return report
	}
	report.Endpoint, report.HTTPHost = connection.Endpoint, connection.HTTPHost
	report.ScopeID, report.AgentID, report.ExecutionID = connection.ScopeID, connection.AgentID, connection.ExecutionID
	report.ExpiresAt = connection.ExpiresAt.UTC().Format(time.RFC3339)
	report.HookCredential = connection.HookToken != ""
	if class, problem := probeManagedEndpoint(ctx, connection); class != "" {
		report.Status, report.Problem = string(class), problem
		return report
	}
	report.Status = "ok"
	return report
}

// probeManagedEndpoint sends one JSON-RPC ping with the execution credential.
// A ping is not an MCP initialize, so it cannot count as the harness connecting
// or mark the agent ready, and it never reserves or drains inbox messages. The
// Bus daemon and Desktop Core are stateless for this request; the go-sdk
// handler closes an uninitialized session as soon as the request ends.
func probeManagedEndpoint(ctx context.Context, connection managedMCPConnection) (connectionFailure, string) {
	client := &http.Client{CheckRedirect: refuseMCPRedirect}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, connection.Endpoint, bytes.NewReader([]byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)))
	if err != nil {
		return failureConfiguration, "the MCP endpoint could not be requested"
	}
	request.Header.Set("Authorization", "Bearer "+connection.AgentToken)
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Content-Type", "application/json")
	if connection.HTTPHost != "" {
		request.Host = connection.HTTPHost
	}
	response, err := client.Do(request)
	if err != nil {
		return classifyTransportError(err)
	}
	defer response.Body.Close()
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		data, err := io.ReadAll(io.LimitReader(response.Body, failureBodyLimit+1))
		var reply struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      int             `json:"id"`
			Result  json.RawMessage `json:"result"`
			Error   json.RawMessage `json:"error"`
		}
		var pong map[string]any
		if err != nil || len(data) > failureBodyLimit || json.Unmarshal(data, &reply) != nil || reply.JSONRPC != "2.0" || reply.ID != 1 || reply.Error != nil || json.Unmarshal(reply.Result, &pong) != nil || pong == nil {
			return failureProtocol, "the endpoint did not return a valid MCP ping response"
		}
		return "", ""
	}
	return classifyHTTPFailure(response.StatusCode, readFailureBody(response))
}

func printConnectionCheck(out io.Writer, report connectionCheck) {
	fmt.Fprintf(out, "Managed connection: %s\n", report.Status)
	fmt.Fprintf(out, "Runtime %s, protocol %s\n", report.RuntimeVersion, report.ProtocolVersion)
	if report.Endpoint != "" {
		host := ""
		if report.HTTPHost != "" {
			host = " (Host " + report.HTTPHost + ")"
		}
		fmt.Fprintf(out, "Endpoint: %s%s\n", report.Endpoint, host)
		fmt.Fprintf(out, "Scope: %s, agent: %s, execution: %s\n", report.ScopeID, report.AgentID, report.ExecutionID)
		fmt.Fprintf(out, "Credential expires: %s\n", report.ExpiresAt)
		hook := "absent"
		if report.HookCredential {
			hook = "present"
		}
		fmt.Fprintf(out, "Hook credential: %s\n", hook)
	}
	if report.Problem != "" {
		fmt.Fprintf(out, "Problem: %s\n", report.Problem)
	}
	fmt.Fprintln(out, strings.TrimSpace("This proves route and credential admission only; agent readiness comes from the host controller's process evidence."))
}
