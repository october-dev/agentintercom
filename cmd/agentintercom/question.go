package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/october-dev/agentintercom/bus"
)

// The server's limit for an answer (validateText in bus/runtime.go).
const maxAnswerBytes = 16384

// Questions are answered with the local workspace owner credential. An agent
// execution credential is never read here and the server refuses one, so an
// agent's own credential cannot answer its question.
func questionCommand(args []string, in io.Reader, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("question requires a subcommand: list or answer")
	}
	switch args[0] {
	case "list":
		return listQuestions(args[1:], out)
	case "answer":
		return answerQuestion(args[1:], in, out)
	}
	return fmt.Errorf("unknown question subcommand %q; use list or answer", args[0])
}

func listQuestions(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("question list", flag.ContinueOnError)
	workspace := flags.String("workspace", "", "local workspace name")
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *workspace == "" {
		return errors.New("question list requires --workspace <name> and no positional arguments")
	}
	owner, err := localScopeClient(*workspace)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	all, err := owner.ListEscalations(ctx)
	if err != nil {
		return err
	}
	pending := []bus.HumanEscalation{}
	for _, question := range all {
		if question.Status == "pending" {
			pending = append(pending, question)
		}
	}
	if *jsonOutput {
		return json.NewEncoder(out).Encode(pending)
	}
	if len(pending) == 0 {
		_, err = fmt.Fprintln(out, "No pending questions.")
		return err
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "QUESTION\tSESSION\tASKED")
	for _, question := range pending {
		fmt.Fprintf(w, "%s\t%s\t%s\n", question.ID, question.AgentID, question.CreatedAt)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	for _, question := range pending {
		// Agent text is untrusted: quoting keeps it from controlling the terminal.
		if _, err := fmt.Fprintf(out, "\n%s asks:\n  %q\n", question.ID, question.Question); err != nil {
			return err
		}
		if len(question.Options) > 0 {
			quoted := make([]string, len(question.Options))
			for i, option := range question.Options {
				quoted[i] = fmt.Sprintf("%q", option)
			}
			if _, err := fmt.Fprintf(out, "  Choices: %s\n", strings.Join(quoted, ", ")); err != nil {
				return err
			}
		}
	}
	_, err = fmt.Fprintf(out, "\nAnswer with: agentintercom question answer --workspace %s --id <question-id> --answer <text>\n", *workspace)
	return err
}

func answerQuestion(args []string, in io.Reader, out io.Writer) error {
	flags := flag.NewFlagSet("question answer", flag.ContinueOnError)
	workspace := flags.String("workspace", "", "local workspace name")
	id := flags.String("id", "", "question ID from question list")
	answer := flags.String("answer", "", "answer text")
	file := flags.String("answer-file", "", "answer file, or - for stdin")
	jsonOutput := flags.Bool("json", false, "print the answered question as JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *workspace == "" || *id == "" {
		return errors.New("question answer requires --workspace <name> --id <question-id> and no positional arguments")
	}
	text, err := textInput(*answer, *file, in, "answer", maxAnswerBytes)
	if err != nil {
		return err
	}
	owner, err := localScopeClient(*workspace)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resolved, err := owner.ResolveEscalation(ctx, *id, text)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return json.NewEncoder(out).Encode(resolved)
	}
	_, err = fmt.Fprintf(out, "Answered %s. The agent %s receives the answer in its inbox.\n", resolved.ID, resolved.AgentID)
	return err
}
