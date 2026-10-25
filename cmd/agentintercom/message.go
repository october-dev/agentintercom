package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/october-dev/agent-intercom/bus"
)

// Execution commands use an agent credential supplied by the launcher, never a
// workspace owner credential. They do not create or impersonate another session.
func executionClient(address string) (bus.Client, error) {
	token := os.Getenv("OCTOBER_BUS_AGENT_TOKEN")
	if token == "" {
		return bus.Client{}, errors.New("this command needs a connected session: OCTOBER_BUS_AGENT_TOKEN is not set")
	}
	address, err := resolveBusAddress(address)
	if err != nil {
		return bus.Client{}, err
	}
	u, err := url.Parse(address)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return bus.Client{}, errors.New("Bus address must be an HTTPS origin or a local loopback URL")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && ip != nil && ip.IsLoopback()) {
		return bus.Client{}, errors.New("remote Bus connections require HTTPS")
	}
	return bus.Client{Address: strings.TrimRight(address, "/"), Token: token,
		HTTP: &http.Client{Timeout: 30 * time.Second, CheckRedirect: refuseMCPRedirect}}, nil
}

func messageBody(body, file string, input io.Reader) (string, error) {
	if file != "" {
		if body != "" {
			return "", errors.New("use either --body or --body-file")
		}
		if file != "-" {
			f, err := os.Open(file)
			if err != nil {
				return "", err
			}
			defer f.Close()
			input = f
		}
		data, err := io.ReadAll(io.LimitReader(input, 65537))
		if err != nil {
			return "", err
		}
		body = string(data)
	}
	if strings.TrimSpace(body) == "" || len(body) > 65536 {
		return "", errors.New("message body must contain 1 to 65536 bytes; use --body or --body-file (use - for stdin)")
	}
	return body, nil
}

func sendMessageCommand(args []string, in io.Reader, out io.Writer) error {
	flags := flag.NewFlagSet("message send", flag.ContinueOnError)
	address := flags.String("address", "", "Bus address")
	to := flags.String("to", "", "exact recipient session ID")
	body := flags.String("body", "", "message text")
	file := flags.String("body-file", "", "message file, or - for stdin")
	mode := flags.String("mode", "notify", "notify or request; --reply-to selects response")
	replyTo := flags.String("reply-to", "", "delivered request message ID")
	key := flags.String("key", "", "idempotency key; reuse for a retry of the same message")
	expiry := flags.Duration("expires", 0, "optional message expiry, for example 1h")
	jsonOutput := flags.Bool("json", false, "print the receipt as JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *to == "" {
		return errors.New("message send requires --to <session-id> and no positional arguments")
	}
	if *mode != "notify" && *mode != "request" {
		return errors.New("--mode must be notify or request; use --reply-to for responses")
	}
	if *replyTo != "" {
		if *mode != "notify" {
			return errors.New("--reply-to cannot be combined with --mode request")
		}
		*mode = "response"
	}
	if *expiry < 0 || *expiry > 30*24*time.Hour || (*expiry > 0 && expiry.Milliseconds() == 0) {
		return errors.New("--expires must be zero or between 1ms and 720h")
	}
	text, err := messageBody(*body, *file, in)
	if err != nil {
		return err
	}
	client, err := executionClient(*address)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	receipt, err := client.SendMessage(ctx, bus.SendMessageInput{To: *to, Body: text, Mode: bus.MessageMode(*mode), ResponseTo: *replyTo, IdempotencyKey: *key, ExpiresInMS: expiry.Milliseconds()})
	if err != nil {
		return err
	}
	if *jsonOutput {
		return json.NewEncoder(out).Encode(receipt)
	}
	_, err = fmt.Fprintf(out, "Accepted %s (%s). Acceptance does not mean the recipient has read it.\n", receipt.MessageID, receipt.State)
	return err
}

func inboxCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("inbox", flag.ContinueOnError)
	follow := flags.Bool("follow", false, "watch pending messages without delivering or acknowledging them")
	address := flags.String("address", "", "Bus address")
	limit := flags.Int("limit", 20, "maximum messages to receive")
	wait := flags.Duration("wait", 0, "wait up to 25s for messages")
	jsonOutput := flags.Bool("json", false, "print messages as JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *wait < 0 || *wait > 25*time.Second {
		return errors.New("inbox accepts flags only; --wait must be between 0 and 25s")
	}
	client, err := executionClient(*address)
	if err != nil {
		return err
	}
	if *follow {
		if *wait != 0 {
			return errors.New("--follow uses its own polling interval; omit --wait")
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		return followInbox(ctx, client, out, *jsonOutput)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	messages, err := client.PullInbox(ctx, *limit, *wait)
	if err != nil {
		return err
	}
	if messages == nil {
		messages = []bus.Message{}
	}
	if *jsonOutput {
		return json.NewEncoder(out).Encode(messages)
	}
	for _, message := range messages {
		// Quoting prevents a peer's message from executing terminal escapes.
		if _, err := fmt.Fprintf(out, "%s from %s (%s)\n  %q\n", message.ID, message.From, message.Mode, message.Body); err != nil {
			return err
		}
	}
	if len(messages) == 0 {
		_, err = fmt.Fprintln(out, "No messages.")
	} else {
		_, err = fmt.Fprintln(out, "Messages remain available until acknowledged after processing: agentintercom message ack <message-id>")
	}
	return err
}

func followInbox(ctx context.Context, client bus.Client, out io.Writer, jsonOutput bool) error {
	seen := map[string]bool{}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		notices, err := client.PreviewInbox(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		current := make(map[string]bool, len(notices))
		for i := len(notices) - 1; i >= 0; i-- {
			n := notices[i]
			current[n.ID] = true
			if seen[n.ID] {
				continue
			}
			if jsonOutput {
				err = json.NewEncoder(out).Encode(n)
			} else {
				_, err = fmt.Fprintf(out, "%s from %q: %q\n", n.ID, n.From, strings.SplitN(n.Preview, "\n", 2)[0])
			}
			if err != nil {
				return err
			}
		}
		seen = current
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func acknowledgeCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("message ack", flag.ContinueOnError)
	address := flags.String("address", "", "Bus address")
	jsonOutput := flags.Bool("json", false, "print count as JSON")
	if err := flags.Parse(receiptFlagArgs(args)); err != nil {
		return err
	}
	if flags.NArg() == 0 {
		return errors.New("message ack requires one or more processed message IDs")
	}
	client, err := executionClient(*address)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	count, err := client.AcknowledgeMessages(ctx, flags.Args())
	if err != nil {
		return err
	}
	if *jsonOutput {
		return json.NewEncoder(out).Encode(map[string]int64{"acknowledged": count})
	}
	_, err = fmt.Fprintf(out, "Acknowledged %d messages.\n", count)
	return err
}
