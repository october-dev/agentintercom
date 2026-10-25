package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/october-dev/agent-intercom/bus"
)

func databaseCommand(args []string, out io.Writer) error {
	if len(args) == 0 || (args[0] != "migrate" && args[0] != "check") {
		return errors.New("use database migrate or database check [--url-env <environment-variable>]")
	}
	flags := flag.NewFlagSet("database "+args[0], flag.ContinueOnError)
	variable := flags.String("url-env", "OCTOBER_BUS_DATABASE_URL", "environment variable containing the PostgreSQL connection URL")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || *variable == "" || os.Getenv(*variable) == "" {
		return errors.New("configure the PostgreSQL URL in the selected environment variable; never pass credentials as arguments")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if args[0] == "migrate" {
		if err := bus.MigratePostgres(ctx, os.Getenv(*variable)); err != nil {
			return err
		}
		_, err := fmt.Fprintln(out, "PostgreSQL migrations applied.")
		return err
	}
	runtime, err := bus.OpenPostgres(ctx, os.Getenv(*variable))
	if err != nil {
		return err
	}
	defer runtime.Close()
	_, err = fmt.Fprintln(out, "PostgreSQL connection and schema are ready.")
	return err
}
