package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/october-dev/october-bus/bus"
)

func scopeAdmin(operation string, args []string) error {
	flags := flag.NewFlagSet("scope "+operation, flag.ContinueOnError)
	address := flags.String("address", "", "Agent Intercom address")
	id := flags.String("id", "", "scope ID")
	confirm := flags.String("confirm", "", "confirm the exact scope ID for deletion")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || (operation != "list" && *id == "") {
		return errors.New("scope operation requires --id and no positional arguments")
	}
	if operation == "delete" && (*confirm == "" || *confirm != *id) {
		return errors.New("scope delete requires --confirm matching --id; deletion cannot be undone without a backup")
	}
	client, err := adminClient(*address)
	if err != nil {
		return err
	}
	if operation != "list" {
		unlock, err := lockLocalScopeCredentials(client.Address)
		if err != nil {
			return err
		}
		defer unlock()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var result any
	switch operation {
	case "list":
		result, err = client.ListScopes(ctx)
	case "rotate-token":
		result, err = client.RotateScopeToken(ctx, *id)
	case "delete":
		err = client.DeleteScope(ctx, *id)
		result = map[string]string{"deletedScopeId": *id}
	default:
		return errors.New("unknown scope admin operation")
	}
	if err != nil {
		return err
	}
	if operation == "rotate-token" {
		credential := result.(bus.CreateScopeResult)
		if err := updateLocalScopeCredential(client.Address, credential.ScopeID, credential.ScopeToken); err != nil {
			return err
		}
	} else if operation == "delete" {
		if err := updateLocalScopeCredential(client.Address, *id, ""); err != nil {
			return err
		}
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}

// Only cache credentials for the locally discovered daemon, never a remote URL.
func lockLocalScopeCredentials(address string) (func(), error) {
	paths, err := bus.DefaultDaemonPaths()
	if err != nil {
		return nil, err
	}
	run, err := bus.ReadRunFile(paths.RunFile)
	if os.IsNotExist(err) || (err == nil && run.Address != address) {
		return func() {}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot validate local discovery before credential mutation: %w", err)
	}
	lock, err := bus.LockScopeCredentials(paths.DataDir)
	if err != nil {
		return nil, err
	}
	return func() { _ = lock.Close() }, nil
}

func updateLocalScopeCredential(address, scopeID, token string) error {
	paths, err := bus.DefaultDaemonPaths()
	if err != nil {
		return err
	}
	run, err := bus.ReadRunFile(paths.RunFile)
	if os.IsNotExist(err) || (err == nil && run.Address != address) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("scope changed but local discovery could not be validated; repair discovery and rotate again: %w", err)
	}
	if token == "" {
		err = bus.RemoveScopeToken(paths.DataDir, scopeID)
	} else {
		err = bus.SaveScopeToken(paths.DataDir, scopeID, token)
	}
	if err != nil {
		return fmt.Errorf("scope changed on the daemon but local credential update failed; repair the data directory before reconnecting: %w", err)
	}
	return nil
}

func localScopeClient(scopeID string) (bus.Client, error) {
	paths, err := bus.DefaultDaemonPaths()
	if err != nil {
		return bus.Client{}, err
	}
	return localScopeClientAt(paths, scopeID)
}

func localScopeClientAt(paths bus.DaemonPaths, scopeID string) (bus.Client, error) {
	run, err := bus.ReadRunFile(paths.RunFile)
	if err != nil {
		return bus.Client{}, fmt.Errorf("local daemon is unavailable; run agentintercom start: %w", err)
	}
	token, err := bus.ReadScopeToken(paths.DataDir, scopeID)
	if err != nil {
		return bus.Client{}, err
	}
	return bus.Client{Address: run.Address, Token: token}, nil
}

func linkAgents(args []string) error {
	flags := flag.NewFlagSet("link", flag.ContinueOnError)
	scope := flags.String("scope", "", "local scope ID")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *scope == "" || flags.NArg() != 2 {
		return errors.New("link requires --scope <scope-id> <agent-a> <agent-b>; start both agents first")
	}
	client, err := localScopeClient(*scope)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.LinkAgents(ctx, flags.Arg(0), flags.Arg(1)); err != nil {
		return fmt.Errorf("could not link agents (both must be registered): %w", err)
	}
	fmt.Printf("Linked %s and %s in scope %s\n", flags.Arg(0), flags.Arg(1), *scope)
	return nil
}

func backupDatabase(args []string) error {
	flags := flag.NewFlagSet("backup", flag.ContinueOnError)
	bundle := flags.Bool("bundle", false, "include attachment bytes with the SQLite snapshot in a tar.gz bundle")
	address := flags.String("address", "", "Agent Intercom address")
	output := flags.String("output", "", "new private SQLite backup path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *output == "" || flags.NArg() != 0 {
		return errors.New("backup requires --output and no positional arguments")
	}
	client, err := adminClient(*address)
	if err != nil {
		return err
	}
	client.HTTP = &http.Client{Timeout: 5 * time.Minute}
	file, err := os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	complete := false
	defer func() {
		file.Close()
		if !complete {
			_ = os.Remove(*output)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var backupErr error
	if *bundle {
		backupErr = client.BackupBundleTo(ctx, file)
	} else {
		backupErr = client.BackupTo(ctx, file)
	}
	if err := backupErr; err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	complete = true
	if *bundle {
		fmt.Printf("Database and attachment bundle saved to %s. It contains credentials; keep it private.\n", *output)
	} else {
		fmt.Printf("Database snapshot saved to %s. Attachment files are separate; use --bundle to include them. This snapshot contains credentials; keep it private.\n", *output)
	}
	return nil
}
