package main

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/october-dev/october-bus/bus"
)

func hostedCommand(args []string) error {
	if len(args) == 0 || args[0] != "serve" {
		return errors.New("use hosted serve --public-url <https-origin> [--listen 127.0.0.1:4780]")
	}
	flags := flag.NewFlagSet("hosted serve", flag.ContinueOnError)
	listen := flags.String("listen", "127.0.0.1:4780", "loopback listener behind the TLS reverse proxy")
	public := flags.String("public-url", "", "public HTTPS origin")
	var origins, issuers stringList
	flags.Var(&origins, "allow-origin", "additional exact HTTPS browser origin; repeatable")
	flags.Var(&issuers, "auth-issuer", "additional exact trusted Auth issuer; repeatable")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("hosted serve accepts flags only")
	}
	host, _, err := net.SplitHostPort(*listen)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return errors.New("hosted listener must use a loopback IP; publish it through the TLS proxy")
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv("OCTOBER_BUS_ENCRYPTION_KEY")))
	if err != nil || len(key) != 32 {
		return errors.New("OCTOBER_BUS_ENCRYPTION_KEY must contain a base64-encoded 32-byte secret")
	}
	identity, err := bus.NewSupabaseIdentity(os.Getenv("OCTOBER_BUS_AUTH_URL"), os.Getenv("OCTOBER_BUS_AUTH_PUBLIC_KEY"), issuers)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	runtime, err := bus.OpenPostgres(ctx, os.Getenv("OCTOBER_BUS_DATABASE_URL"))
	cancel()
	if err != nil {
		return err
	}
	defer runtime.Close()
	accounts, err := bus.NewHostedAccounts(runtime, key)
	if err != nil {
		return err
	}
	handler, err := bus.NewHostedServer(accounts, bus.HostedServerOptions{PublicURL: *public, AllowedOrigins: origins, Identity: identity})
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 * 1024}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	fmt.Printf("Hosted October Bus listening on %s for %s\n", listener.Addr(), *public)
	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var serveErr error
	select {
	case <-signalCtx.Done():
	case serveErr = <-done:
	}
	handler.CancelWaits()
	shutdown, stopShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopShutdown()
	shutdownErr := server.Shutdown(shutdown)
	closeErr := handler.Close(shutdown)
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}
	if shutdownErr != nil {
		return shutdownErr
	}
	return closeErr
}
