package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"github.com/october-dev/agentintercom/bus"
	"io"
	"time"
)

func channelCommand(args []string, in io.Reader, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("use channel list/create/join/leave/post/read")
	}
	flags := flag.NewFlagSet("channel "+args[0], flag.ContinueOnError)
	address := flags.String("address", "", "Bus address")
	id := flags.String("id", "", "channel ID")
	name := flags.String("name", "", "new channel name")
	description := flags.String("description", "", "new channel description")
	body := flags.String("body", "", "post text")
	file := flags.String("body-file", "", "post file, or - for stdin")
	thread := flags.String("thread", "", "root post ID for a thread")
	key := flags.String("key", "", "idempotency key for safe retries")
	query := flags.String("query", "", "literal text search; omit --id to search all channels")
	before := flags.String("before", "", "pagination cursor")
	limit := flags.Int("limit", 50, "history page size, 1–100")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("channel commands accept flags only")
	}
	client, err := executionClient(*address)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var result any
	switch args[0] {
	case "list":
		result, err = client.Channels(ctx)
	case "create":
		result, err = client.CreateChannel(ctx, bus.CreateChannelInput{Name: *name, Description: *description})
	case "join", "leave":
		err = client.SubscribeChannel(ctx, *id, args[0] == "join")
		result = map[string]bool{"subscribed": args[0] == "join"}
	case "post":
		var text string
		text, err = messageBody(*body, *file, in)
		if err == nil {
			result, err = client.PostChannel(ctx, bus.PostChannelInput{ChannelID: *id, Body: text, ThreadID: *thread, IdempotencyKey: *key})
		}
	case "read":
		result, err = client.ChannelHistory(ctx, bus.ChannelHistoryInput{ChannelID: *id, ThreadID: *thread, Query: *query, Before: *before, Limit: *limit})
	default:
		return errors.New("use channel list/create/join/leave/post/read")
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(result)
}
