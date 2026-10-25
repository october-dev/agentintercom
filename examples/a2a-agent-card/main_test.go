package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/october-dev/october-bus/a2abridge"
)

func TestAgentCardServer(t *testing.T) {
	card, err := a2abridge.NewAgentCard(a2abridge.AgentProfile{
		DisplayName: "Test Agent",
		Capabilities: []a2abridge.Capability{
			{Name: "test_cap", Description: "A test capability."},
		},
	}, a2abridge.CardOptions{
		InterfaceURL: "http://127.0.0.1:0",
	})
	if err != nil {
		t.Fatalf("NewAgentCard: %v", err)
	}

	handler, err := a2abridge.NewAgentCardHandler(card)
	if err != nil {
		t.Fatalf("NewAgentCardHandler: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle(a2abridge.AgentCardPath, handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + a2abridge.AgentCardPath)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected Content-Type application/json, got %q", ct)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if name, ok := body["name"].(string); !ok || name != "Test Agent" {
		t.Fatalf("unexpected name: %v", body["name"])
	}

	skills, ok := body["skills"].([]any)
	if !ok || len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %v", body["skills"])
	}

	fmt.Fprintf(os.Stderr, "TestAgentCardServer: card endpoint returned valid agent card JSON\n")
}
