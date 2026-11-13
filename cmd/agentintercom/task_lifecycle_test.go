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

func requireTaskCode(t *testing.T, err error, code bus.ErrorCode) {
	t.Helper()
	var busErr *bus.BusError
	require(t, errors.As(err, &busErr) && busErr.Code == code, "expected %s, got %T: %v", code, err, err)
}

func runTask(t *testing.T, token string, in string, args ...string) (string, error) {
	t.Helper()
	t.Setenv("OCTOBER_BUS_AGENT_TOKEN", token)
	var out bytes.Buffer
	err := taskLifecycleCommand(args[0], args[1:], strings.NewReader(in), &out)
	return out.String(), err
}

func TestTaskCLIClaimProgressComplete(t *testing.T) {
	ctx, owner := startWorkspaceTest(t)
	a, err := owner.RegisterAgent(ctx, bus.RegisterAgentInput{ID: "a", DisplayName: "First"})
	requireNoError(t, err)
	b, err := owner.RegisterAgent(ctx, bus.RegisterAgentInput{ID: "b", DisplayName: "Second"})
	requireNoError(t, err)
	t.Setenv("OCTOBER_BUS_ADDRESS", owner.Address)
	t.Setenv("OCTOBER_BUS_SCOPE_TOKEN", "")
	build, err := owner.AddTask(ctx, bus.AddTaskInput{Title: "Build"})
	requireNoError(t, err)
	ship, err := owner.AddTask(ctx, bus.AddTaskInput{Title: "Ship", Dependencies: []string{build.ID}})
	requireNoError(t, err)

	out, err := runTask(t, a.AgentToken, "", "claim", "--id", build.ID, "--json")
	requireNoError(t, err)
	var claimed bus.Task
	requireNoError(t, json.Unmarshal([]byte(out), &claimed))
	require(t, claimed.ClaimedBy == "a", "claim not recorded for a: %#v", claimed)

	// Another session cannot take a claimed task, and an unmet dependency blocks a claim.
	_, err = runTask(t, b.AgentToken, "", "claim", "--id", build.ID)
	requireTaskCode(t, err, bus.CodeConflict)
	_, err = runTask(t, b.AgentToken, "", "claim", "--id", ship.ID)
	requireTaskCode(t, err, bus.CodeConflict)
	// Only the claiming execution can release or complete it.
	_, err = runTask(t, b.AgentToken, "", "release", "--id", build.ID)
	requireTaskCode(t, err, bus.CodeConflict)
	_, err = runTask(t, b.AgentToken, "", "complete", "--id", build.ID)
	requireTaskCode(t, err, bus.CodeConflict)

	// Progress text from stdin, never on the command line.
	out, err = runTask(t, a.AgentToken, "half done\x1b[2J", "progress", "add", "--id", build.ID, "--text-file", "-", "--json")
	requireNoError(t, err)
	var entry bus.TaskProgress
	requireNoError(t, json.Unmarshal([]byte(out), &entry))
	require(t, entry.Kind == "progress" && entry.Text == "half done\x1b[2J" && entry.AgentID == "a", "wrong progress entry: %#v", entry)
	_, err = runTask(t, a.AgentToken, "", "progress", "add", "--id", build.ID, "--kind", "blocker", "--text", "waiting on review")
	requireNoError(t, err)

	// Any session in the workspace can read progress; agent text is quoted.
	out, err = runTask(t, b.AgentToken, "", "progress", "list", "--id", build.ID)
	requireNoError(t, err)
	require(t, !strings.Contains(out, "\x1b") && strings.Contains(out, `"half done\x1b[2J"`) && strings.Contains(out, "blocker by a"), "progress list unsafe or incomplete: %q", out)
	out, err = runTask(t, b.AgentToken, "", "progress", "list", "--id", build.ID, "--json")
	requireNoError(t, err)
	var entries []bus.TaskProgress
	requireNoError(t, json.Unmarshal([]byte(out), &entries))
	require(t, len(entries) == 2, "expected 2 progress entries: %#v", entries)

	// The workspace owner can read the same history without an agent session.
	t.Setenv("OCTOBER_BUS_SCOPE_TOKEN", owner.Token)
	out, err = runTask(t, "", "", "progress", "list", "--id", build.ID, "--json")
	requireNoError(t, err)
	requireNoError(t, json.Unmarshal([]byte(out), &entries))
	require(t, len(entries) == 2, "owner could not read progress: %#v", entries)
	t.Setenv("OCTOBER_BUS_SCOPE_TOKEN", "")

	// Release and reclaim keep ownership with the claiming execution.
	_, err = runTask(t, a.AgentToken, "", "release", "--id", build.ID)
	requireNoError(t, err)
	_, err = runTask(t, a.AgentToken, "", "claim", "--id", build.ID)
	requireNoError(t, err)

	notes := filepath.Join(t.TempDir(), "notes.txt")
	requireNoError(t, os.WriteFile(notes, []byte("built and tested"), 0o600))
	out, err = runTask(t, a.AgentToken, "", "complete", "--id", build.ID, "--note-file", notes, "--json")
	requireNoError(t, err)
	var completed bus.Task
	requireNoError(t, json.Unmarshal([]byte(out), &completed))
	require(t, completed.Note == "built and tested" && completed.Status != claimed.Status, "completion not recorded: %#v", completed)

	// The dependency is now met, so the other session can claim the next task.
	out, err = runTask(t, b.AgentToken, "", "claim", "--id", ship.ID)
	requireNoError(t, err)
	require(t, strings.Contains(out, "Claimed "+ship.ID), "unexpected claim output: %s", out)
}

func TestTaskCLIUsesOnlyTheExecutionCredential(t *testing.T) {
	ctx, owner := startWorkspaceTest(t)
	first, err := owner.RegisterAgent(ctx, bus.RegisterAgentInput{ID: "a", DisplayName: "First"})
	requireNoError(t, err)
	t.Setenv("OCTOBER_BUS_ADDRESS", owner.Address)
	task, err := owner.AddTask(ctx, bus.AddTaskInput{Title: "Build"})
	requireNoError(t, err)

	// A workspace owner credential does not substitute for an agent session.
	t.Setenv("OCTOBER_BUS_SCOPE_TOKEN", owner.Token)
	_, err = runTask(t, "", "", "claim", "--id", task.ID)
	require(t, err != nil && strings.Contains(err.Error(), "OCTOBER_BUS_AGENT_TOKEN"), "claimed without an agent session: %v", err)
	_, err = runTask(t, owner.Token, "", "claim", "--id", task.ID)
	require(t, err != nil, "owner credential claimed a task as an agent")

	// A replaced execution loses its claim authority.
	_, err = runTask(t, first.AgentToken, "", "claim", "--id", task.ID)
	requireNoError(t, err)
	_, err = owner.RegisterAgent(ctx, bus.RegisterAgentInput{ID: "a", DisplayName: "First"})
	requireNoError(t, err)
	_, err = runTask(t, first.AgentToken, "", "complete", "--id", task.ID)
	require(t, err != nil, "replaced execution completed a task")
	_, err = runTask(t, first.AgentToken, "", "progress", "add", "--id", task.ID, "--text", "late")
	require(t, err != nil, "replaced execution added progress")
	listed, err := owner.ListTasks(ctx, false)
	requireNoError(t, err)
	require(t, len(listed) == 1 && listed[0].Note == "", "replaced execution changed the task: %#v", listed)
}

func TestTaskCLIAddAndListInsideAnAgentSession(t *testing.T) {
	ctx, owner := startWorkspaceTest(t)
	a, err := owner.RegisterAgent(ctx, bus.RegisterAgentInput{ID: "a", DisplayName: "First"})
	requireNoError(t, err)
	// The environment `agent run` gives its child: an address and an agent token only.
	t.Setenv("OCTOBER_BUS_ADDRESS", owner.Address)
	t.Setenv("OCTOBER_BUS_SCOPE_TOKEN", "")
	t.Setenv("OCTOBER_BUS_AGENT_TOKEN", a.AgentToken)
	requireNoError(t, addTask([]string{"--title", "Write docs", "--json"}))
	tasks, err := owner.ListTasks(ctx, false)
	requireNoError(t, err)
	require(t, len(tasks) == 1 && tasks[0].CreatedBy != nil && *tasks[0].CreatedBy == "a", "task not added as the session: %#v", tasks)
	requireNoError(t, listTasks([]string{"--ready", "--json"}))

	// When both are set, the workspace token keeps precedence, as before this change.
	t.Setenv("OCTOBER_BUS_SCOPE_TOKEN", owner.Token)
	requireNoError(t, addTask([]string{"--title", "Owner task", "--json"}))
	tasks, err = owner.ListTasks(ctx, false)
	requireNoError(t, err)
	require(t, len(tasks) == 2 && tasks[1].Title == "Owner task" && tasks[1].CreatedBy == nil, "workspace token lost precedence: %#v", tasks)
	t.Setenv("OCTOBER_BUS_SCOPE_TOKEN", "")

	// Without either credential the error names both ways to provide one.
	t.Setenv("OCTOBER_BUS_AGENT_TOKEN", "")
	err = addTask([]string{"--title", "x"})
	require(t, err != nil && strings.Contains(err.Error(), "OCTOBER_BUS_SCOPE_TOKEN") && strings.Contains(err.Error(), "agent run"), "unclear credential error: %v", err)
	err = listTasks(nil)
	require(t, err != nil && strings.Contains(err.Error(), "OCTOBER_BUS_SCOPE_TOKEN"), "unclear credential error: %v", err)
}

func TestTaskCLIInputValidation(t *testing.T) {
	text, err := textInput("", "-", strings.NewReader(strings.Repeat("a", maxTaskProgressBytes)), "text", maxTaskProgressBytes)
	requireNoError(t, err)
	require(t, len(text) == maxTaskProgressBytes, "rejected text at the limit")

	for name, run := range map[string]func() error{
		"both inputs": func() error {
			_, err := textInput("a", "-", strings.NewReader("b"), "text", maxTaskProgressBytes)
			return err
		},
		"over the limit": func() error {
			_, err := textInput("", "-", strings.NewReader(strings.Repeat("a", maxTaskProgressBytes+1)), "text", maxTaskProgressBytes)
			return err
		},
		"empty progress": func() error { _, err := textInput(" \n", "", nil, "text", maxTaskProgressBytes); return err },
		"missing file": func() error {
			_, err := textInput("", filepath.Join(t.TempDir(), "none"), nil, "note", maxTaskNoteBytes)
			return err
		},
		"progress without subcommand": func() error { return taskLifecycleCommand("progress", nil, nil, &bytes.Buffer{}) },
		"unknown progress subcommand": func() error {
			return taskLifecycleCommand("progress", []string{"delete"}, nil, &bytes.Buffer{})
		},
	} {
		require(t, run() != nil, "%s: accepted invalid input", name)
	}
	// Checked by message, so they cannot pass because of a missing credential instead.
	t.Setenv("OCTOBER_BUS_AGENT_TOKEN", "")
	for args, want := range map[string]string{
		"claim":                      "requires --id",
		"release --id t extra":       "requires --id",
		"complete":                   "requires --id",
		"progress add --text x":      "requires --id",
		"progress list --id t extra": "requires --id",
		"progress add --id t --kind done --text x": "--kind must be",
		"progress add --id t":                      "text must contain 1 to",
		"complete --id t --note a --note-file -":   "use either --note or --note-file",
	} {
		fields := strings.Fields(args)
		err := taskLifecycleCommand(fields[0], fields[1:], strings.NewReader(""), &bytes.Buffer{})
		require(t, err != nil && strings.Contains(err.Error(), want), "%s: want %q, got %v", args, want, err)
	}
}
