package main

import (
	"bytes"
	"encoding/json"
	"github.com/october-dev/october-bus/bus"
	"strings"
	"testing"
)

func TestChannelCLIUsesExecutionAndLiteralBodies(t *testing.T) {
	ctx, owner := startWorkspaceTest(t)
	agent, err := owner.RegisterAgent(ctx, bus.RegisterAgentInput{ID: "terminal", DisplayName: "Terminal"})
	requireNoError(t, err)
	t.Setenv("OCTOBER_BUS_ADDRESS", owner.Address)
	t.Setenv("OCTOBER_BUS_AGENT_TOKEN", agent.AgentToken)
	var out bytes.Buffer
	requireNoError(t, channelCommand([]string{"create", "--name", "shared"}, nil, &out))
	var channel bus.Channel
	requireNoError(t, json.Unmarshal(out.Bytes(), &channel))
	out.Reset()
	requireNoError(t, channelCommand([]string{"join", "--id", channel.ID}, nil, &out))
	out.Reset()
	text := "Literal % _ and control " + string(rune(27)) + "[31m"
	requireNoError(t, channelCommand([]string{"post", "--id", channel.ID, "--body-file", "-", "--key", "post-once"}, strings.NewReader(text), &out))
	out.Reset()
	requireNoError(t, channelCommand([]string{"read", "--query", "% _"}, nil, &out))
	var page bus.ChannelPostPage
	requireNoError(t, json.Unmarshal(out.Bytes(), &page))
	require(t, len(page.Posts) == 1 && page.Posts[0].Body == text, "CLI lost literal text")
	require(t, !strings.Contains(out.String(), string(rune(27))), "CLI allowed terminal control text")
}
