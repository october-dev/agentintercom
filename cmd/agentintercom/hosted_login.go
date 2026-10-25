package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/october-dev/agent-intercom/bus"
)

type hostedProfile struct {
	Version     int    `json:"version"`
	Server      string `json:"server"`
	WorkspaceID string `json:"workspaceId"`
	DeviceID    string `json:"deviceId"`
	Token       string `json:"token"`
	ExpiresAt   string `json:"expiresAt"`
}

var hostedProfileName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

func hostedHTTPClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("hosted redirect refused") }}
}
func hostedOrigin(value string) (string, error) {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", errors.New("hosted server must be an HTTPS origin, without a path, credentials, query or fragment")
	}
	return strings.TrimRight(value, "/"), nil
}
func hostedProfilePath(name string, create bool) (string, error) {
	if !hostedProfileName.MatchString(name) {
		return "", errors.New("profile name must use 1–64 letters, digits, underscores or hyphens")
	}
	if runtime.GOOS == "windows" {
		return "", errors.New("CLI device credentials currently require macOS, Linux or WSL; use the remote MCP connector on Windows")
	}
	paths, err := bus.DefaultDaemonPaths()
	if err != nil {
		return "", err
	}
	dir, err := filepath.Abs(filepath.Join(paths.DataDir, "hosted"))
	if err != nil {
		return "", err
	}
	if create {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return "", err
		}
	}
	if err := nativePrivateDirectory(dir); err != nil {
		return "", err
	}
	return filepath.Join(dir, name+".json"), nil
}
func readHostedProfile(name string) (hostedProfile, error) {
	var profile hostedProfile
	path, err := hostedProfilePath(name, false)
	if err != nil {
		return profile, fmt.Errorf("hosted profile unavailable; run agentintercom hosted login --profile %s", name)
	}
	if err := readPrivateSessionJSON(path, &profile); err != nil {
		return profile, errors.New("hosted profile is missing or is not a private credential file")
	}
	origin, err := hostedOrigin(profile.Server)
	expiry, expiryErr := time.Parse(time.RFC3339Nano, profile.ExpiresAt)
	if err != nil || profile.Version != 1 || profile.WorkspaceID == "" || profile.DeviceID == "" || !strings.HasPrefix(profile.Token, "obd_") || !validExecutionToken(strings.TrimPrefix(profile.Token, "obd_")) || expiryErr != nil {
		return profile, errors.New("hosted profile is invalid; reconnect the device")
	}
	if !expiry.After(time.Now()) {
		return profile, errors.New("hosted device credential expired; reconnect the device")
	}
	profile.Server = origin
	return profile, nil
}
func hostedDeviceClient(name string) (bus.Client, error) {
	profile, err := readHostedProfile(name)
	if err != nil {
		return bus.Client{}, err
	}
	return bus.Client{Address: profile.Server, Token: profile.Token, HTTP: hostedHTTPClient()}, nil
}

func hostedLogin(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("hosted login", flag.ContinueOnError)
	server := flags.String("server", "https://bus.october.dev", "hosted Bus origin")
	profileName := flags.String("profile", "default", "local connection name")
	deviceName := flags.String("name", "", "device label shown to workspace members")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("hosted login accepts flags only")
	}
	origin, err := hostedOrigin(*server)
	if err != nil {
		return err
	}
	path, err := hostedProfilePath(*profileName, true)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		return errors.New("profile already exists; log it out or choose a different --profile")
	} else if !os.IsNotExist(err) {
		return err
	}
	if info, err := os.Lstat(path + ".pending"); err == nil && info.Mode().IsRegular() && time.Since(info.ModTime()) > 15*time.Minute {
		_ = os.Remove(path + ".pending")
	}
	lock, err := os.OpenFile(path+".pending", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return errors.New("a login is already pending for this profile; finish it or use another profile")
	}
	lock.Close()
	defer os.Remove(path + ".pending")
	if *deviceName == "" {
		*deviceName, err = os.Hostname()
		if err != nil || *deviceName == "" {
			*deviceName = "My machine"
		}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ctx, timeout := context.WithTimeout(ctx, 11*time.Minute)
	defer timeout()
	client := bus.Client{Address: origin, HTTP: hostedHTTPClient()}
	pending, err := client.BeginDevicePairing(ctx, *deviceName)
	if err != nil {
		return err
	}
	// Build the URL from the configured origin and validated code, never from an
	// arbitrary server-supplied link. The device secret is never printed.
	if !regexp.MustCompile(`^[A-Z2-7]{4}-[A-Z2-7]{4}$`).MatchString(pending.UserCode) {
		return errors.New("hosted service returned an invalid verification code")
	}
	deadline, err := time.Parse(time.RFC3339Nano, pending.ExpiresAt)
	if err != nil {
		return errors.New("hosted service returned an invalid approval deadline")
	}
	fmt.Fprintf(out, "Open %s/app#device=%s\nConfirm code %s and choose a workspace. This request expires in 10 minutes.\n", origin, pending.UserCode, pending.UserCode)
	for time.Now().Before(deadline) {
		timer := time.NewTimer(5 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		result, err := client.PollDevicePairing(ctx, pending.DeviceCode)
		if err != nil {
			if code := bus.AsBusError(err).Code; code == bus.CodeBackpressure || code == bus.CodeInternal {
				continue
			}
			return err
		}
		switch result.Status {
		case "pending":
			continue
		case "denied":
			return errors.New("device connection was declined")
		case "approved":
			if result.Device == nil || result.WorkspaceID == "" {
				return errors.New("hosted service returned an incomplete connection")
			}
			profile := hostedProfile{Version: 1, Server: origin, WorkspaceID: result.WorkspaceID, DeviceID: result.Device.ID, Token: result.Device.Token, ExpiresAt: result.Device.ExpiresAt}
			if err := saveHostedProfile(path, profile); err != nil {
				return fmt.Errorf("could not save device credential; revoke this connection in Members & devices: %w", err)
			}
			fmt.Fprintf(out, "Connected profile %q.\nNext: agentintercom connect --hosted %s\n", *profileName, *profileName)
			return nil
		default:
			return errors.New("hosted service returned an unknown approval status")
		}
	}
	return errors.New("device connection expired; run hosted login again")
}
func saveHostedProfile(path string, profile hostedProfile) error {
	if _, err := hostedOrigin(profile.Server); err != nil {
		return err
	}
	if !strings.HasPrefix(profile.Token, "obd_") || !validExecutionToken(strings.TrimPrefix(profile.Token, "obd_")) {
		return errors.New("invalid device credential")
	}
	if err := nativePrivateDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	complete := false
	defer func() {
		file.Close()
		if !complete {
			os.Remove(path)
		}
	}()
	if err := json.NewEncoder(file).Encode(profile); err != nil {
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
func hostedLogout(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("hosted logout", flag.ContinueOnError)
	name := flags.String("profile", "default", "local connection name")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("hosted logout accepts flags only")
	}
	path, err := hostedProfilePath(*name, false)
	if err != nil {
		return err
	}
	var profile hostedProfile
	if err := readPrivateSessionJSON(path, &profile); err != nil {
		return err
	}
	origin, err := hostedOrigin(profile.Server)
	if err != nil || !strings.HasPrefix(profile.Token, "obd_") || !validExecutionToken(strings.TrimPrefix(profile.Token, "obd_")) {
		return errors.New("invalid hosted profile")
	}
	client := bus.Client{Address: origin, Token: profile.Token, HTTP: hostedHTTPClient()}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.RevokeDevice(ctx); err != nil && bus.AsBusError(err).Code != bus.CodeUnauthenticated {
		return fmt.Errorf("remote revocation failed; credential retained so you can retry: %w", err)
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "Revoked and removed hosted profile %q.\n", *name)
	return err
}
