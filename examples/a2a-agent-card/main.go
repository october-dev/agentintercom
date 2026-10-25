// Example A2A Agent Card server.
//
// Run with:
//
//	go run ./examples/a2a-agent-card
//
// Then curl:
//
//	curl http://127.0.0.1:8080/.well-known/agent-card.json
//
// The PORT environment variable overrides the default (8080).
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/october-dev/october-bus/a2abridge"
)

const defaultPort = "8080"

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = defaultPort
	}

	card, err := a2abridge.NewAgentCard(a2abridge.AgentProfile{
		DisplayName: "Agent Intercom Example Agent",
		Capabilities: []a2abridge.Capability{
			{Name: "chat", Description: "Conversational chat capability."},
			{Name: "code_review", Description: "Reviews code changes for correctness and style."},
		},
	}, a2abridge.CardOptions{
		InterfaceURL: "http://127.0.0.1:" + port,
	})
	if err != nil {
		log.Fatalf("creating agent card: %v", err)
	}

	handler, err := a2abridge.NewAgentCardHandler(card)
	if err != nil {
		log.Fatalf("creating handler: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle(a2abridge.AgentCardPath, handler)

	addr := "127.0.0.1:" + port
	fmt.Printf("Agent Card server listening on http://%s%s\n", addr, a2abridge.AgentCardPath)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("server: %v", err)
	}
}
