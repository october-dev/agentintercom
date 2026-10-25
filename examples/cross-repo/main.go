package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/october-dev/october-bus/bus"
)

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, output io.Writer) error {
	frontendDirectory, backendDirectory, err := workingDirectories()
	if err != nil {
		return err
	}

	runtimeValue, err := bus.Open(":memory:")
	if err != nil {
		return err
	}
	defer runtimeValue.Close()

	adminToken, err := randomToken()
	if err != nil {
		return err
	}
	server := bus.NewServer(runtimeValue, bus.ServerOptions{AdminToken: adminToken})
	address, err := server.Start()
	if err != nil {
		return err
	}
	defer server.Stop(context.Background())

	admin := bus.Client{Address: address, Token: adminToken}
	scope, err := admin.CreateScope(ctx, bus.CreateScopeInput{ID: "cross-repo-example"})
	if err != nil {
		return err
	}
	scopeClient := bus.Client{Address: address, Token: scope.ScopeToken}
	backendRegistration, err := scopeClient.RegisterAgent(ctx, bus.RegisterAgentInput{
		ID: "backend", DisplayName: "Backend agent",
		Capabilities: []bus.AgentCapability{{Name: "api_contracts"}},
	})
	if err != nil {
		return err
	}
	frontendRegistration, err := scopeClient.RegisterAgent(ctx, bus.RegisterAgentInput{
		ID: "frontend", DisplayName: "Frontend agent",
		Capabilities: []bus.AgentCapability{{Name: "frontend_integration"}},
		ConnectTo:    []string{"backend"},
	})
	if err != nil {
		return err
	}
	frontend := bus.Client{Address: address, Token: frontendRegistration.AgentToken}
	backend := bus.Client{Address: address, Token: backendRegistration.AgentToken}
	for _, client := range []bus.Client{frontend, backend} {
		if _, err := client.Heartbeat(ctx, bus.HeartbeatInput{Lifecycle: bus.LifecycleReady, Ready: true}); err != nil {
			return err
		}
	}

	frontendPeers, err := frontend.ListPeers(ctx)
	if err != nil {
		return err
	}
	backendPeers, err := backend.ListPeers(ctx)
	if err != nil {
		return err
	}
	if len(frontendPeers) != 1 || len(backendPeers) != 1 {
		return fmt.Errorf("unexpected peer discovery: frontend=%d backend=%d", len(frontendPeers), len(backendPeers))
	}
	fmt.Fprintf(output, "workspaces: frontend=%s backend=%s\n", frontendDirectory, backendDirectory)
	fmt.Fprintf(output, "discovery: frontend found %s; backend found %s\n", frontendPeers[0].ID, backendPeers[0].ID)

	contract := "GET /api/profile adds displayName: string; existing fields and status code remain unchanged."
	request, err := frontend.SendMessage(ctx, bus.SendMessageInput{
		To: "backend", Mode: bus.MessageRequest,
		Body:    "Please add displayName to the profile response.",
		Context: []bus.ContextItem{{Kind: "text", Title: "Requested API contract", Text: contract}},
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "bounded context: %s\n", contract)
	fmt.Fprintf(output, "delegation: request %s accepted\n", request.MessageID)

	backendTask, err := frontend.AddTask(ctx, bus.AddTaskInput{
		Title:       "Add displayName to the profile API",
		Description: contract,
	})
	if err != nil {
		return err
	}
	frontendTask, err := frontend.AddTask(ctx, bus.AddTaskInput{
		Title:        "Render displayName in the profile UI",
		Description:  "Consume only the agreed API contract.",
		Dependencies: []string{backendTask.ID},
	})
	if err != nil {
		return err
	}
	if _, err := frontend.ClaimTask(ctx, frontendTask.ID); err == nil {
		return fmt.Errorf("frontend task was claimable before its dependency completed")
	}
	fmt.Fprintf(output, "dependencies: frontend task %s waits for backend task %s\n", frontendTask.ID, backendTask.ID)

	if _, err := backend.ClaimTask(ctx, backendTask.ID); err != nil {
		return err
	}
	requests, err := backend.PullInbox(ctx, 10, 0)
	if err != nil {
		return err
	}
	if len(requests) != 1 {
		return fmt.Errorf("backend received %d requests, want 1", len(requests))
	}
	if _, err := backend.AcknowledgeMessages(ctx, []string{requests[0].ID}); err != nil {
		return err
	}
	if _, err := backend.CompleteTask(ctx, backendTask.ID, "API contract implemented"); err != nil {
		return err
	}
	reply, err := backend.SendMessage(ctx, bus.SendMessageInput{
		To: "frontend", Mode: bus.MessageResponse, ResponseTo: requests[0].ID,
		Body:    "The profile response now includes displayName.",
		Context: []bus.ContextItem{{Kind: "text", Title: "Implemented API contract", Text: contract}},
	})
	if err != nil {
		return err
	}

	replies, err := frontend.PullInbox(ctx, 10, 0)
	if err != nil {
		return err
	}
	if len(replies) != 1 {
		return fmt.Errorf("frontend received %d replies, want 1", len(replies))
	}
	if _, err := frontend.AcknowledgeMessages(ctx, []string{replies[0].ID}); err != nil {
		return err
	}
	if _, err := frontend.ClaimTask(ctx, frontendTask.ID); err != nil {
		return err
	}
	if _, err := frontend.CompleteTask(ctx, frontendTask.ID, "UI integrated against the contract"); err != nil {
		return err
	}

	requestReceipt, err := frontend.Receipt(ctx, request.MessageID)
	if err != nil {
		return err
	}
	replyReceipt, err := backend.Receipt(ctx, reply.MessageID)
	if err != nil {
		return err
	}
	tasks, err := scopeClient.ListTasks(ctx, false)
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "reply: %s\n", replies[0].Body)
	fmt.Fprintf(output, "receipts: request=%s response=%s\n", requestReceipt.State, replyReceipt.State)
	for _, task := range tasks {
		fmt.Fprintf(output, "task state: %s=%s\n", task.Title, task.Status)
	}
	return nil
}

func workingDirectories() (string, string, error) {
	root, err := os.Getwd()
	if err != nil {
		return "", "", err
	}
	if _, err := os.Stat(filepath.Join(root, "frontend")); err != nil {
		root = filepath.Join(root, "examples", "cross-repo")
	}
	frontend := filepath.Join(root, "frontend")
	backend := filepath.Join(root, "backend")
	for _, directory := range []string{frontend, backend} {
		info, err := os.Stat(directory)
		if err != nil {
			return "", "", fmt.Errorf("working directory %s: %w", directory, err)
		}
		if !info.IsDir() {
			return "", "", fmt.Errorf("working directory %s is not a directory", directory)
		}
	}
	return frontend, backend, nil
}

func randomToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}
