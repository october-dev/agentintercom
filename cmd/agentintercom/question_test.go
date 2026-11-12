package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/october-dev/agentintercom/bus"
)

func requireBusCode(t *testing.T, err error, code bus.ErrorCode) {
	t.Helper()
	var busErr *bus.BusError
	require(t, errors.As(err, &busErr) && busErr.Code == code, "expected %s, got %T: %v", code, err, err)
}

func TestQuestionAskListAnswerAndAgentReadsAnswer(t *testing.T) {
	ctx, owner := startWorkspaceTest(t)
	registered, err := owner.RegisterAgent(ctx, bus.RegisterAgentInput{ID: "worker", DisplayName: "Worker"})
	requireNoError(t, err)
	agent := bus.Client{Address: owner.Address, Token: registered.AgentToken}
	asked, err := agent.AskHuman(ctx, bus.AskHumanInput{Question: "Deploy now?\x1b[2J", Options: []string{"yes", "no\x07"}})
	requireNoError(t, err)

	var output bytes.Buffer
	requireNoError(t, questionCommand([]string{"list", "--workspace", "project", "--json"}, nil, &output))
	var listed []bus.HumanEscalation
	requireNoError(t, json.Unmarshal(output.Bytes(), &listed))
	require(t, len(listed) == 1 && listed[0].ID == asked.ID && listed[0].AgentID == "worker" && len(listed[0].Options) == 2, "wrong pending list: %#v", listed)
	require(t, !strings.Contains(output.String(), owner.Token), "question list leaked the owner credential")

	output.Reset()
	requireNoError(t, questionCommand([]string{"list", "--workspace", "project"}, nil, &output))
	text := output.String()
	require(t, strings.Contains(text, asked.ID) && strings.Contains(text, "worker"), "missing question row: %s", text)
	require(t, !strings.ContainsAny(text, "\x1b\x07"), "agent text reached the terminal unquoted: %q", text)
	require(t, strings.Contains(text, `"Deploy now?\x1b[2J"`) && strings.Contains(text, `"no\a"`), "question or choices not shown quoted: %s", text)

	// The answer comes from stdin, so it never enters shell history.
	output.Reset()
	requireNoError(t, questionCommand([]string{"answer", "--workspace", "project", "--id", asked.ID, "--answer-file", "-", "--json"}, strings.NewReader("yes, after the tests pass"), &output))
	var answered bus.HumanEscalation
	requireNoError(t, json.Unmarshal(output.Bytes(), &answered))
	require(t, answered.Status == "resolved" && answered.Answer == "yes, after the tests pass", "wrong answer result: %#v", answered)

	// The asking agent sees the answer, both on the question and in its inbox.
	seen, err := agent.Escalation(ctx, asked.ID)
	requireNoError(t, err)
	require(t, seen.Status == "resolved" && seen.Answer == "yes, after the tests pass", "agent did not see the answer: %#v", seen)
	messages, err := agent.PullInbox(ctx, 10, 0)
	requireNoError(t, err)
	require(t, len(messages) == 1 && strings.Contains(messages[0].Body, "yes, after the tests pass"), "answer not delivered to the inbox: %#v", messages)

	output.Reset()
	requireNoError(t, questionCommand([]string{"list", "--workspace", "project"}, nil, &output))
	require(t, strings.Contains(output.String(), "No pending questions."), "answered question still listed: %s", output.String())

	// Existing server errors are preserved, not replaced with new ones.
	err = questionCommand([]string{"answer", "--workspace", "project", "--id", asked.ID, "--answer", "again"}, nil, &output)
	requireBusCode(t, err, bus.CodeConflict)
	err = questionCommand([]string{"answer", "--workspace", "project", "--id", "esc_missing", "--answer", "x"}, nil, &output)
	requireBusCode(t, err, bus.CodeNotFound)
}

func TestQuestionAnswerAuthority(t *testing.T) {
	ctx, owner := startWorkspaceTest(t)
	registered, err := owner.RegisterAgent(ctx, bus.RegisterAgentInput{ID: "worker", DisplayName: "Worker"})
	requireNoError(t, err)
	agent := bus.Client{Address: owner.Address, Token: registered.AgentToken}
	asked, err := agent.AskHuman(ctx, bus.AskHumanInput{Question: "Approve?"})
	requireNoError(t, err)

	// An agent execution credential cannot list or answer questions itself.
	_, err = agent.ListEscalations(ctx)
	require(t, err != nil, "agent credential listed workspace questions")
	_, err = agent.ResolveEscalation(ctx, asked.ID, "approved by myself")
	require(t, err != nil, "agent credential answered its own question")

	// The command ignores an agent token in the environment and never falls
	// back to inherited remote credentials (startWorkspaceTest sets untrusted ones).
	t.Setenv("OCTOBER_BUS_AGENT_TOKEN", registered.AgentToken)
	var output bytes.Buffer
	require(t, questionCommand([]string{"list", "--workspace", "missing"}, nil, &output) != nil, "listed questions for a workspace with no local credential")

	// Another local workspace's owner cannot see or answer this workspace's question.
	requireNoError(t, workspaceCommand([]string{"create", "other"}, &output))
	output.Reset()
	requireNoError(t, questionCommand([]string{"list", "--workspace", "other", "--json"}, nil, &output))
	require(t, strings.TrimSpace(output.String()) == "[]", "foreign workspace saw the question: %s", output.String())
	err = questionCommand([]string{"answer", "--workspace", "other", "--id", asked.ID, "--answer", "yes"}, nil, &output)
	requireBusCode(t, err, bus.CodeNotFound)

	still, err := agent.Escalation(ctx, asked.ID)
	requireNoError(t, err)
	require(t, still.Status == "pending", "question was answered by the wrong authority: %#v", still)
}

func TestQuestionCommandInputValidation(t *testing.T) {
	file := filepath.Join(t.TempDir(), "answer.txt")
	requireNoError(t, os.WriteFile(file, []byte("from a file"), 0o600))
	text, err := textInput("", file, nil, "answer", maxAnswerBytes)
	requireNoError(t, err)
	require(t, text == "from a file", "wrong file answer: %q", text)

	limit := strings.Repeat("a", maxAnswerBytes)
	text, err = textInput("", "-", strings.NewReader(limit), "answer", maxAnswerBytes)
	requireNoError(t, err)
	require(t, len(text) == maxAnswerBytes, "rejected an answer at the size limit")

	for name, run := range map[string]func() error{
		"both inputs": func() error { _, err := textInput("a", file, nil, "answer", maxAnswerBytes); return err },
		"empty":       func() error { _, err := textInput("  \n", "", nil, "answer", maxAnswerBytes); return err },
		"over the limit": func() error {
			_, err := textInput("", "-", strings.NewReader(limit+"a"), "answer", maxAnswerBytes)
			return err
		},
		"missing file": func() error {
			_, err := textInput("", filepath.Join(t.TempDir(), "none"), nil, "answer", maxAnswerBytes)
			return err
		},
		"no subcommand":   func() error { return questionCommand(nil, nil, &bytes.Buffer{}) },
		"unknown command": func() error { return questionCommand([]string{"delete"}, nil, &bytes.Buffer{}) },
		"answer without id": func() error {
			return questionCommand([]string{"answer", "--workspace", "w", "--answer", "x"}, nil, &bytes.Buffer{})
		},
		"list without workspace": func() error { return questionCommand([]string{"list"}, nil, &bytes.Buffer{}) },
	} {
		require(t, run() != nil, "%s: accepted invalid input", name)
	}
	// Checked by message: a missing workspace would also fail, for the wrong reason.
	for _, args := range [][]string{
		{"list", "--workspace", "w", "extra"},
		{"answer", "--workspace", "w", "--id", "q", "--answer", "x", "extra"},
	} {
		err := questionCommand(args, nil, &bytes.Buffer{})
		require(t, err != nil && strings.Contains(err.Error(), "no positional arguments"), "%v: positional argument not rejected: %v", args, err)
	}
}
