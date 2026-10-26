package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/october-dev/agentintercom/bus"
)

func TestAttachmentCLIUploadDownloadAndNoOverwrite(t *testing.T) {
	r, err := bus.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()
	scope, err := r.CreateScope(ctx, bus.CreateScopeInput{})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := r.RegisterAgent(ctx, scope.ScopeToken, bus.RegisterAgentInput{ID: "files", DisplayName: "Files"})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(bus.NewServer(r, bus.ServerOptions{}))
	defer server.Close()
	t.Setenv("OCTOBER_BUS_AGENT_TOKEN", agent.AgentToken)
	var out bytes.Buffer
	data := []byte{0, 255, 27, 91, 65}
	err = attachmentCommand([]string{"upload", "--address", server.URL, "--file", "-", "--name", "payload.bin", "--key", "once"}, bytes.NewReader(data), &out)
	if err != nil {
		t.Fatal(err)
	}
	var file bus.Attachment
	if err := json.Unmarshal(out.Bytes(), &file); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	destination := filepath.Join(t.TempDir(), "download.bin")
	args := []string{"download", "--address", server.URL, "--id", file.ID, "--file", destination}
	if err := attachmentCommand(args, nil, &out); err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(destination)
	if err != nil || !bytes.Equal(actual, data) {
		t.Fatal("download did not preserve binary bytes", err)
	}
	if err := attachmentCommand(args, nil, &out); err == nil {
		t.Fatal("existing destination was overwritten")
	}
	if bytes.Contains(out.Bytes(), data) {
		t.Fatal("binary bytes were printed to the terminal")
	}
}
