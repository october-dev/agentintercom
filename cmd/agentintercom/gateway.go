package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/october-dev/october-bus/bus"
	"github.com/october-dev/october-bus/internal/gateway"
)

func gatewayCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("use gateway init, gateway key, or gateway start")
	}
	switch args[0] {
	case "init":
		return initGateway(args[1:])
	case "key":
		flags := flag.NewFlagSet("gateway key", flag.ContinueOnError)
		output := flags.String("output", "", "new private API key file")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *output == "" || flags.NArg() != 0 {
			return errors.New("gateway key requires --output <new-file>")
		}
		digest, err := writeGatewayKey(*output)
		if err == nil {
			fmt.Printf("API key saved to %s. Set apiKeySha256 to %s and restart the gateway.\n", *output, digest)
		}
		return err
	case "start":
		return startGateway(args[1:])
	default:
		return errors.New("use gateway init, gateway key, or gateway start")
	}
}

func writeNewPrivateFile(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	complete := false
	defer func() {
		file.Close()
		if !complete {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	complete = true
	return nil
}

func writeGatewayKey(path string) (string, error) {
	var entropy [32]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", err
	}
	key := "obg_" + base64.RawURLEncoding.EncodeToString(entropy[:])
	digest := sha256.Sum256([]byte(key))
	if err := writeNewPrivateFile(path, []byte(key+"\n")); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest[:]), nil
}

func initGateway(args []string) error {
	flags := flag.NewFlagSet("gateway init", flag.ContinueOnError)
	publicURL := flags.String("public-url", "", "public HTTPS origin, e.g. https://bus.october.dev")
	scope := flags.String("scope", "", "existing locally provisioned scope")
	agent := flags.String("agent", "muse", "dedicated connector identity")
	name := flags.String("name", "Muse connector", "connector display name")
	configPath := flags.String("config", "", "new gateway configuration file")
	keyPath := flags.String("key-file", "", "new private API key file; key is never logged")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *configPath == "" || *keyPath == "" || *keyPath == *configPath {
		return errors.New("gateway init requires distinct --config and --key-file paths")
	}
	config := gateway.Config{PublicURL: *publicURL, Clients: []gateway.ClientConfig{{Scope: *scope, Agent: *agent, Name: *name, APIKeySHA256: strings.Repeat("0", 64)}}}
	if err := config.Validate(); err != nil {
		return err
	}
	paths, err := bus.DefaultDaemonPaths()
	if err != nil {
		return err
	}
	if _, err := bus.ReadScopeToken(paths.DataDir, *scope); err != nil {
		return err
	}
	// Do not produce a new secret if configuration already exists.
	if _, err := os.Lstat(*configPath); !os.IsNotExist(err) {
		return errors.New("configuration already exists or cannot be inspected")
	}
	digest, err := writeGatewayKey(*keyPath)
	if err != nil {
		return err
	}
	config.Clients[0].APIKeySHA256 = digest
	data, err := json.MarshalIndent(config, "", "  ")
	if err == nil {
		err = writeNewPrivateFile(*configPath, append(data, '\n'))
	}
	if err != nil {
		_ = os.Remove(*keyPath)
		return err
	}
	fmt.Printf("Created %s and private key file %s. Endpoint after deployment: %s/mcp\n", *configPath, *keyPath, *publicURL)
	return nil
}

func readGatewayConfig(path string) (gateway.Config, error) {
	var config gateway.Config
	info, err := os.Lstat(path)
	if err != nil {
		return config, err
	}
	if !info.Mode().IsRegular() || info.Size() > 128*1024 || (runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0) {
		return config, errors.New("gateway config must be a private regular file of at most 128 KiB")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return config, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return config, errors.New("invalid gateway configuration JSON")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return config, errors.New("gateway config must contain one JSON object")
	}
	return config, config.Validate()
}

// daemonStartupWait bounds how long `gateway start` waits for the local daemon.
// A service manager orders the gateway after the daemon process, not after the
// daemon is ready; failing immediately would trip restart limits on boot.
const daemonStartupWait = 60 * time.Second

// awaitDaemon returns the daemon's run file once the daemon answers its health
// probe, retrying until the deadline. It never registers anything.
func awaitDaemon(ctx context.Context, runFile string, wait time.Duration) (bus.RunFile, error) {
	deadline := time.Now().Add(wait)
	var lastErr error
	announced := false
	for {
		run, err := bus.ReadRunFile(runFile)
		if err == nil {
			probe, cancel := context.WithTimeout(ctx, 3*time.Second)
			_, err = (bus.Client{Address: run.Address, HTTP: &http.Client{CheckRedirect: rejectBusRedirect}}).Health(probe)
			cancel()
			if err == nil {
				return run, nil
			}
		}
		lastErr = err
		if time.Now().After(deadline) {
			return bus.RunFile{}, fmt.Errorf("start the local Bus daemon first: %w", lastErr)
		}
		if !announced {
			fmt.Println("Waiting for the local Bus daemon to become ready")
			announced = true
		}
		select {
		case <-ctx.Done():
			return bus.RunFile{}, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func startGateway(args []string) (runErr error) {
	flags := flag.NewFlagSet("gateway start", flag.ContinueOnError)
	configPath := flags.String("config", "", "private gateway configuration file")
	listen := flags.String("listen", "127.0.0.1:8787", "loopback listener behind an HTTPS reverse proxy")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *configPath == "" {
		return errors.New("gateway start requires --config")
	}
	host, _, err := net.SplitHostPort(*listen)
	ip, parseErr := netip.ParseAddr(host)
	if err != nil || parseErr != nil || !ip.IsLoopback() {
		return errors.New("gateway must listen on a loopback IP behind an HTTPS reverse proxy")
	}
	config, err := readGatewayConfig(*configPath)
	if err != nil {
		return err
	}
	paths, err := bus.DefaultDaemonPaths()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	run, err := awaitDaemon(ctx, paths.RunFile, daemonStartupWait)
	if err != nil {
		return err
	}
	// Bind first: a second gateway must not replace active executions and then
	// discover that the first gateway already owns this listener.
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	g, err := gateway.New(ctx, run.Address, config, func(scope string) (string, error) {
		return bus.ReadScopeToken(paths.DataDir, scope)
	})
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		runErr = errors.Join(runErr, g.Close(cleanup))
	}()
	// ReadTimeout also bounds the post-response drain of a withheld request
	// body: the gateway flushes rejections before reading a body, but net/http
	// still discards up to 256 KiB of it on the connection goroutine afterwards.
	server := &http.Server{Handler: g, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 35 * time.Second, WriteTimeout: 40 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024}
	finished := make(chan error, 1)
	go func() { finished <- server.Serve(listener) }()
	fmt.Printf("Gateway listening on %s for %s/mcp\n", listener.Addr(), config.PublicURL)
	select {
	case <-ctx.Done():
	case <-g.Done():
		runErr = errors.New("connector execution ended; restart the gateway after checking the daemon and credentials")
	case err := <-finished:
		if !errors.Is(err, http.ErrServerClosed) {
			runErr = err
		}
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil {
		_ = server.Close()
		runErr = errors.Join(runErr, err)
	}
	return runErr
}
