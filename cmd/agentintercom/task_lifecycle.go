package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/october-dev/agentintercom/bus"
)

// Server limits for task text (validateText in bus/runtime.go).
const (
	maxTaskNoteBytes     = 16384
	maxTaskProgressBytes = 4000
)

// Task lifecycle commands act as the connected agent session. They use only the
// execution credential from `agent run` or a harness (OCTOBER_BUS_AGENT_TOKEN),
// never a workspace owner or administrator credential, so the server's
// ownership rules (one claimant, release and complete by that execution only)
// stay in force.
func taskLifecycleCommand(operation string, args []string, in io.Reader, out io.Writer) error {
	switch operation {
	case "claim", "release":
		return claimOrReleaseTask(operation, args, out)
	case "complete":
		return completeTask(args, in, out)
	case "progress":
		if len(args) == 0 {
			return errors.New("task progress requires a subcommand: add or list")
		}
		switch args[0] {
		case "add":
			return addTaskProgress(args[1:], in, out)
		case "list":
			return listTaskProgress(args[1:], out)
		}
		return fmt.Errorf("unknown task progress subcommand %q; use add or list", args[0])
	}
	return fmt.Errorf("unknown task subcommand %q", operation)
}

func claimOrReleaseTask(operation string, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("task "+operation, flag.ContinueOnError)
	id := flags.String("id", "", "task ID")
	address := flags.String("address", "", "Agent Intercom address")
	jsonOutput := flags.Bool("json", false, "print the task as JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *id == "" {
		return fmt.Errorf("task %s requires --id <task-id> and no positional arguments", operation)
	}
	client, err := executionClient(*address)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var task bus.Task
	if operation == "claim" {
		task, err = client.ClaimTask(ctx, *id)
	} else {
		task, err = client.ReleaseTask(ctx, *id)
	}
	if err != nil {
		return err
	}
	if *jsonOutput {
		return json.NewEncoder(out).Encode(task)
	}
	verb := map[string]string{"claim": "Claimed", "release": "Released"}[operation]
	_, err = fmt.Fprintf(out, "%s %s %q (%s).\n", verb, task.ID, task.Title, task.Status)
	return err
}

func completeTask(args []string, in io.Reader, out io.Writer) error {
	flags := flag.NewFlagSet("task complete", flag.ContinueOnError)
	id := flags.String("id", "", "task ID")
	note := flags.String("note", "", "optional completion note")
	noteFile := flags.String("note-file", "", "completion note file, or - for stdin")
	address := flags.String("address", "", "Agent Intercom address")
	jsonOutput := flags.Bool("json", false, "print the task as JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *id == "" {
		return errors.New("task complete requires --id <task-id> and no positional arguments")
	}
	text, err := taskText(*note, *noteFile, in, "--note", maxTaskNoteBytes, false)
	if err != nil {
		return err
	}
	client, err := executionClient(*address)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	task, err := client.CompleteTask(ctx, *id, text)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return json.NewEncoder(out).Encode(task)
	}
	_, err = fmt.Fprintf(out, "Completed %s %q.\n", task.ID, task.Title)
	return err
}

func addTaskProgress(args []string, in io.Reader, out io.Writer) error {
	flags := flag.NewFlagSet("task progress add", flag.ContinueOnError)
	id := flags.String("id", "", "task ID")
	kind := flags.String("kind", "progress", "progress, note, or blocker")
	text := flags.String("text", "", "progress text")
	textFile := flags.String("text-file", "", "progress text file, or - for stdin")
	address := flags.String("address", "", "Agent Intercom address")
	jsonOutput := flags.Bool("json", false, "print the progress entry as JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *id == "" {
		return errors.New("task progress add requires --id <task-id> and no positional arguments")
	}
	if *kind != "progress" && *kind != "note" && *kind != "blocker" {
		return errors.New("--kind must be progress, note, or blocker")
	}
	body, err := taskText(*text, *textFile, in, "--text", maxTaskProgressBytes, true)
	if err != nil {
		return err
	}
	client, err := executionClient(*address)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	entry, err := client.AddTaskProgress(ctx, *id, bus.AddTaskProgressInput{Kind: *kind, Text: body})
	if err != nil {
		return err
	}
	if *jsonOutput {
		return json.NewEncoder(out).Encode(entry)
	}
	_, err = fmt.Fprintf(out, "Added %s #%d to %s.\n", entry.Kind, entry.Sequence, entry.TaskID)
	return err
}

func listTaskProgress(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("task progress list", flag.ContinueOnError)
	id := flags.String("id", "", "task ID")
	address := flags.String("address", "", "Agent Intercom address")
	jsonOutput := flags.Bool("json", false, "print progress entries as JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *id == "" {
		return errors.New("task progress list requires --id <task-id> and no positional arguments")
	}
	client, err := executionClient(*address)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	entries, err := client.ListTaskProgress(ctx, *id)
	if err != nil {
		return err
	}
	if entries == nil {
		entries = []bus.TaskProgress{}
	}
	if *jsonOutput {
		return json.NewEncoder(out).Encode(entries)
	}
	if len(entries) == 0 {
		_, err = fmt.Fprintf(out, "No progress on %s.\n", *id)
		return err
	}
	for _, entry := range entries {
		// Progress text comes from other agents: quoting keeps it from controlling the terminal.
		if _, err := fmt.Fprintf(out, "#%d %s by %s at %s\n  %q\n", entry.Sequence, entry.Kind, entry.AgentID, entry.CreatedAt, entry.Text); err != nil {
			return err
		}
	}
	return nil
}

// taskClient serves `task add` and `task list`, which the server accepts from
// either authority (taskAuthority in bus/runtime.go). A workspace token keeps
// precedence, as before; without one, the connected session's execution
// credential is used, which is what `agent run` provides to its child.
func taskClient(address string) (bus.Client, error) {
	if os.Getenv("OCTOBER_BUS_SCOPE_TOKEN") != "" {
		return scopeClient(address)
	}
	if os.Getenv("OCTOBER_BUS_AGENT_TOKEN") != "" {
		return executionClient(address)
	}
	return bus.Client{}, errors.New("this command needs OCTOBER_BUS_SCOPE_TOKEN or a connected session (OCTOBER_BUS_AGENT_TOKEN, set by agent run)")
}

// taskText reads text from exactly one of the inline flag or its file flag, so
// private notes can come from a file or stdin instead of shell history.
func taskText(inline, file string, input io.Reader, name string, limit int, required bool) (string, error) {
	if file != "" {
		if inline != "" {
			return "", fmt.Errorf("use either %s or %s-file", name, name)
		}
		if file != "-" {
			f, err := os.Open(file)
			if err != nil {
				return "", err
			}
			defer f.Close()
			input = f
		}
		data, err := io.ReadAll(io.LimitReader(input, int64(limit)+1))
		if err != nil {
			return "", err
		}
		inline = string(data)
	}
	if len(inline) > limit {
		return "", fmt.Errorf("%s must be at most %d bytes", strings.TrimPrefix(name, "--"), limit)
	}
	if required && strings.TrimSpace(inline) == "" {
		return "", fmt.Errorf("%s is required; use %s or %s-file (use - for stdin)", strings.TrimPrefix(name, "--"), name, name)
	}
	return inline, nil
}
