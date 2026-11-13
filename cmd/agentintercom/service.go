package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/october-dev/agentintercom/bus"
)

const userServiceName = "dev.october.agent-intercom"

// serviceActivationTimeout bounds the post-activation health waits after the
// service manager reports success. Tests override it.
var serviceActivationTimeout = 30 * time.Second

func stableBinaryPath(paths bus.DaemonPaths) string {
	name := "agentintercom"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(paths.DataDir, "bin", name)
}

func stableExecutable() (string, error) {
	dest, _, err := installStableExecutable()
	return dest, err
}

func installStableExecutable() (string, bool, error) {
	source, err := os.Executable()
	if err != nil {
		return "", false, err
	}
	paths, err := bus.DefaultDaemonPaths()
	if err != nil {
		return "", false, err
	}
	dir := filepath.Join(paths.DataDir, "bin")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", false, err
	}
	if err := nativePrivateDirectory(dir); err != nil {
		return "", false, err
	}
	dest := stableBinaryPath(paths)
	if filepath.Clean(source) == filepath.Clean(dest) {
		return dest, false, nil
	}
	if sameExecutableBytes(source, dest) {
		return dest, false, nil
	}
	in, err := os.Open(source)
	if err != nil {
		return "", false, err
	}
	defer in.Close()
	out, err := os.CreateTemp(dir, ".binary-*")
	if err != nil {
		return "", false, err
	}
	defer os.Remove(out.Name())
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return "", false, err
	}
	if err := out.Chmod(0700); err != nil {
		return "", false, err
	}
	if err := out.Sync(); err != nil {
		return "", false, err
	}
	if err := out.Close(); err != nil {
		return "", false, err
	}
	if err := os.Rename(out.Name(), dest); err != nil {
		return "", false, err
	}
	return dest, true, nil
}

// sameExecutableBytes reports whether the installed copy already matches the
// running executable, so reinstalling an unchanged build skips the replacement.
func sameExecutableBytes(source, dest string) bool {
	before, err := os.ReadFile(source)
	if err != nil {
		return false
	}
	after, err := os.ReadFile(dest)
	if err != nil {
		return false
	}
	return bytes.Equal(before, after)
}

// probeDaemon reads the run file and queries the daemon's unauthenticated
// health endpoint for its running version.
func probeDaemon(paths bus.DaemonPaths) (bus.Health, bus.RunFile, error) {
	run, err := bus.ReadRunFile(paths.RunFile)
	if err != nil {
		return bus.Health{}, bus.RunFile{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	health, err := (bus.Client{Address: run.Address}).Health(ctx)
	if err != nil {
		return bus.Health{}, bus.RunFile{}, err
	}
	return health, run, nil
}

// awaitServiceDaemon polls until the daemon answers health and accept agrees, or
// the timeout expires. Activation failures surface here instead of reading as success.
func awaitServiceDaemon(paths bus.DaemonPaths, timeout time.Duration, accept func(bus.Health, bus.RunFile) bool) (bus.Health, bus.RunFile, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		health, run, err := probeDaemon(paths)
		if err == nil && (accept == nil || accept(health, run)) {
			return health, run, nil
		}
		if err != nil {
			lastErr = err
		}
		if time.Now().After(deadline) {
			if lastErr != nil {
				return bus.Health{}, bus.RunFile{}, lastErr
			}
			return bus.Health{}, bus.RunFile{}, errors.New("daemon did not reach the expected state in time")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func serviceFile(platform, home, binary string, paths bus.DaemonPaths) (string, []byte, error) {
	for _, s := range []string{home, binary, paths.DataDir, paths.RuntimeDir} {
		if !filepath.IsAbs(s) || strings.ContainsAny(s, "\x00\r\n") {
			return "", nil, errors.New("service paths must be absolute and contain no control characters")
		}
	}
	switch platform {
	case "linux":
		quote := func(s string) string { return strconv.Quote(strings.ReplaceAll(s, "%", "%%")) }
		body := "# Managed by Agent Intercom\n[Unit]\nDescription=Agent Intercom local daemon\n\n[Service]\nExecStart=" + quote(strings.ReplaceAll(binary, "$", "$$")) + " start\nEnvironment=" + quote("OCTOBER_BUS_DATA_DIR="+paths.DataDir) + "\nEnvironment=" + quote("OCTOBER_BUS_RUNTIME_DIR="+paths.RuntimeDir) + "\nRestart=on-failure\nRestartSec=3\nUMask=0077\n\n[Install]\nWantedBy=default.target\n"
		return filepath.Join(home, ".config", "systemd", "user", userServiceName+".service"), []byte(body), nil
	case "darwin":
		escape := func(s string) string { var b bytes.Buffer; _ = xml.EscapeText(&b, []byte(s)); return b.String() }
		body := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<!-- Managed by Agent Intercom -->
<plist version="1.0"><dict><key>Label</key><string>` + userServiceName + `</string>
<key>ProgramArguments</key><array><string>` + escape(binary) + `</string><string>start</string></array>
<key>EnvironmentVariables</key><dict><key>OCTOBER_BUS_DATA_DIR</key><string>` + escape(paths.DataDir) + `</string><key>OCTOBER_BUS_RUNTIME_DIR</key><string>` + escape(paths.RuntimeDir) + `</string></dict>
<key>RunAtLoad</key><true/><key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
<key>StandardOutPath</key><string>` + escape(filepath.Join(paths.DataDir, "service.log")) + `</string><key>StandardErrorPath</key><string>` + escape(filepath.Join(paths.DataDir, "service.log")) + `</string>
</dict></plist>
`
		return filepath.Join(home, "Library", "LaunchAgents", userServiceName+".plist"), []byte(body), nil
	default:
		return "", nil, errors.New("automatic login services are available on Linux and macOS; on Windows run agentintercom start in your chosen startup task")
	}
}

func serviceCommand(args []string, out io.Writer) error {
	if len(args) != 1 || (args[0] != "install" && args[0] != "uninstall" && args[0] != "status") {
		return errors.New("use service install, service uninstall, or service status")
	}
	return manageService(args[0], out)
}

func manageService(action string, out io.Writer) error {
	return manageServiceOn(runtime.GOOS, action, bus.Version, out)
}

// manageServiceOn runs the login-service action for one platform. Re-running
// install after upgrading the package is the supported upgrade: it replaces
// the stable executable, reactivates the user service, restarts the managed
// daemon only when it is not already on this build, and verifies the running
// version before reporting success.
func manageServiceOn(platform, action, localVersion string, out io.Writer) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	paths, err := bus.DefaultDaemonPaths()
	if err != nil {
		return err
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	binaryChanged := false
	var beforeHealth bus.Health
	var beforeRun bus.RunFile
	wasRunning := false
	if action == "install" {
		if beforeHealth, beforeRun, err = probeDaemon(paths); err == nil {
			wasRunning = true
		}
		binary, binaryChanged, err = installStableExecutable()
		if err != nil {
			return err
		}
	}
	path, body, err := serviceFile(platform, home, binary, paths)
	if err != nil {
		return err
	}
	manager := "systemctl"
	if platform == "darwin" {
		manager = "launchctl"
	}
	old, err := readConfigFile(path)
	if err != nil {
		return err
	}
	if action == "uninstall" && len(old) == 0 {
		return nil
	}
	if _, err := exec.LookPath(manager); err != nil {
		return fmt.Errorf("%s is unavailable; run agentintercom start", manager)
	}
	run := func(args ...string) error {
		c := exec.Command(manager, args...)
		c.Stdout, c.Stderr = out, out
		return c.Run()
	}
	unit := userServiceName + ".service"
	domain := "gui/" + strconv.Itoa(os.Getuid())
	if action == "status" {
		if platform == "linux" {
			return run("--user", "status", unit, "--no-pager")
		}
		return run("print", domain+"/"+userServiceName)
	}
	if len(old) > 0 && !bytes.Contains(old, []byte("Managed by Agent Intercom")) {
		return errors.New("service file is not managed by Agent Intercom; left unchanged")
	}
	if action == "uninstall" {
		if len(old) == 0 {
			return nil
		}
		if platform == "linux" {
			err = run("--user", "disable", "--now", unit)
		} else {
			err = run("bootout", domain, path)
		}
		if err != nil {
			return fmt.Errorf("could not stop the login service; file left intact: %w", err)
		}
		if err = os.Remove(path); err != nil {
			return err
		}
		if platform == "linux" {
			return run("--user", "daemon-reload")
		}
		return nil
	}
	unitChanged := !bytes.Equal(old, body)
	if platform == "darwin" && wasRunning && !binaryChanged && !unitChanged && beforeHealth.RuntimeVersion == localVersion {
		fmt.Fprintln(out, "Login service is already current; the running daemon was left untouched.")
		return nil
	}
	if err = writeConfigFile(path, body); err != nil {
		return err
	}
	if platform == "linux" {
		return activateLinuxService(run, unit, paths, localVersion, binaryChanged, unitChanged, wasRunning, beforeRun, out)
	}
	if len(old) > 0 {
		_ = run("bootout", domain, path)
	}
	if err = run("bootstrap", domain, path); err != nil {
		return fmt.Errorf("service file saved but activation failed; run service install again after restoring your user service manager: %w", err)
	}
	return verifyServiceDaemon(paths, localVersion, wasRunning, beforeRun, "bootstrap", out)
}

// activateLinuxService reloads user systemd, enables the unit, and restarts an
// already-active daemon only when the installed build changed underneath it.
func activateLinuxService(run func(...string) error, unit string, paths bus.DaemonPaths, localVersion string, binaryChanged, unitChanged, wasRunning bool, beforeRun bus.RunFile, out io.Writer) error {
	if err := run("--user", "daemon-reload"); err != nil {
		return fmt.Errorf("service file saved but activation failed; run service install again after restoring your user service manager: %w", err)
	}
	if err := run("--user", "enable", "--now", unit); err != nil {
		return fmt.Errorf("service file saved but activation failed; run service install again after restoring your user service manager: %w", err)
	}
	current, _, err := awaitServiceDaemon(paths, serviceActivationTimeout, nil)
	if err != nil {
		return fmt.Errorf("service file saved but the daemon did not become healthy; check service status and run service install again: %w", err)
	}
	if wasRunning && (binaryChanged || unitChanged || current.RuntimeVersion != localVersion) {
		if err := run("--user", "restart", unit); err != nil {
			return fmt.Errorf("service file saved but restarting the login service failed; the running daemon may still be on the previous build: %w", err)
		}
		return verifyServiceDaemon(paths, localVersion, wasRunning, beforeRun, "restart", out)
	}
	if current.RuntimeVersion != localVersion {
		return fmt.Errorf("service file saved but the running daemon reports version %q instead of %q; check service status and run service install again", current.RuntimeVersion, localVersion)
	}
	if wasRunning {
		fmt.Fprintln(out, "Login service is already current; the running daemon was left untouched.")
		return nil
	}
	fmt.Fprintln(out, "Login service installed. It starts with your user session; Linux startup without a login additionally requires administrator-enabled linger.")
	return nil
}

// managedDaemonNeedsUpgrade reports whether a managed login service exists and
// its running daemon differs from this build, either by reported version or by
// installed bytes. Manually started daemons and unreachable ones are left to
// the existing reuse path; only a positively stale managed daemon upgrades.
func managedDaemonNeedsUpgrade(paths bus.DaemonPaths) (bool, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return false, err
	}
	binary, err := os.Executable()
	if err != nil {
		return false, err
	}
	path, _, err := serviceFile(runtime.GOOS, home, binary, paths)
	if err != nil {
		return false, err
	}
	owned, err := readConfigFile(path)
	if err != nil {
		return false, err
	}
	if len(owned) == 0 || !bytes.Contains(owned, []byte("Managed by Agent Intercom")) {
		return false, nil
	}
	health, _, err := probeDaemon(paths)
	if err != nil {
		return false, nil
	}
	if health.RuntimeVersion != bus.Version {
		return true, nil
	}
	return !sameExecutableBytes(binary, stableBinaryPath(paths)), nil
}

// verifyServiceDaemon waits for the daemon to answer on the new build after a
// restart, requiring a new process identity when one was running before. It
// reports failures instead of letting a stale or missing daemon read as success.
func verifyServiceDaemon(paths bus.DaemonPaths, localVersion string, wasRunning bool, beforeRun bus.RunFile, operation string, out io.Writer) error {
	health, run, err := awaitServiceDaemon(paths, serviceActivationTimeout, func(next bus.Health, nextRun bus.RunFile) bool {
		if next.RuntimeVersion != localVersion {
			return false
		}
		return !wasRunning || nextRun.PID != beforeRun.PID || nextRun.StartedAt != beforeRun.StartedAt
	})
	if err != nil {
		return fmt.Errorf("service file saved but the daemon did not come up on the new build after %s; check service status and run service install again: %w", operation, err)
	}
	if wasRunning {
		fmt.Fprintf(out, "Login service upgraded and restarted on version %s (pid %d). Workspaces and messages are preserved; reconnect MCP in your apps.\n", health.RuntimeVersion, run.PID)
		return nil
	}
	fmt.Fprintln(out, "Login service installed. It starts with your user session; Linux startup without a login additionally requires administrator-enabled linger.")
	return nil
}

func ensureInstallWorkspace(workspace string) error {
	paths, err := bus.DefaultDaemonPaths()
	if err != nil {
		return err
	}
	deadline := time.Now().Add(8 * time.Second)
	for {
		if _, err = bus.ReadRunFile(paths.RunFile); err == nil {
			break
		}
		if time.Now().After(deadline) {
			return errors.New("local daemon is unavailable; run agentintercom start, then install again")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, err = localScopeClientAt(paths, workspace); err == nil {
		return nil
	}
	_, err = createLocalScope(workspace)
	return err
}
