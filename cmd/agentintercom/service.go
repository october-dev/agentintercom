package main

import (
	"bytes"
	"context"
	"encoding/json"
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

// errServiceUnsupported reports that a platform has no automatic login-service
// support. Callers must treat it as "not applicable" (manual daemons keep
// their existing reuse path), never as a failed upgrade.
var errServiceUnsupported = errors.New("automatic login services are available on Linux and macOS; on Windows run agentintercom start in your chosen startup task")

func stableBinaryPath(paths bus.DaemonPaths) string {
	name := "agentintercom"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(paths.DataDir, "bin", name)
}

func stableExecutable() (string, error) {
	source, err := os.Executable()
	if err != nil {
		return "", err
	}
	paths, err := bus.DefaultDaemonPaths()
	if err != nil {
		return "", err
	}
	dest := stableBinaryPath(paths)
	if filepath.Clean(source) != filepath.Clean(dest) && !sameExecutableBytes(source, dest) {
		// Record pending activation BEFORE the new bytes become visible: a
		// copy without activation must not look current later, and a managed
		// daemon's build must never be replaced without evidence the running
		// process is stale. If recording fails, the old bytes stay in place.
		if err := markServiceActivationPending(paths, bus.Version); err != nil {
			return "", err
		}
	}
	installed, _, err := installStableExecutable()
	if err != nil {
		return "", err
	}
	return installed, nil
}

// pendingServicePath locates the marker recording that a service install
// copied a build (or wrote a unit) without verifying the replacement daemon
// yet. Comparing files on disk cannot identify the running build, and a
// same-version retry after a failed activation would otherwise see matching
// files and falsely report "already current".
func pendingServicePath(paths bus.DaemonPaths) string {
	return filepath.Join(paths.DataDir, "service-pending")
}

func pendingServiceActivation(paths bus.DaemonPaths) bool {
	data, err := readConfigFile(pendingServicePath(paths))
	if err != nil {
		// An unreadable or rejected marker is not absence: conservatively
		// require activation so an unevaluated daemon can never read
		// current. The subsequent marker write rechecks the file and fails
		// loudly while it stays unreadable.
		return true
	}
	return len(data) > 0
}

func markServiceActivationPending(paths bus.DaemonPaths, localVersion string) error {
	record, err := json.Marshal(map[string]string{
		"version":   localVersion,
		"createdAt": time.Now().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return err
	}
	return writeConfigFile(pendingServicePath(paths), append(record, '\n'))
}

// clearServiceActivationPending removes the marker best-effort. A stale
// marker only forces an extra restart on the next install; it can never
// produce a false "already current", so clearing must not fail a success.
func clearServiceActivationPending(paths bus.DaemonPaths) {
	_ = os.Remove(pendingServicePath(paths))
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
	var lastHealth bus.Health
	var lastRun bus.RunFile
	observed := false
	for {
		health, run, err := probeDaemon(paths)
		if err == nil && (accept == nil || accept(health, run)) {
			return health, run, nil
		}
		if err != nil {
			lastErr = err
			observed = false
		} else {
			// A reachable daemon reporting the wrong state supersedes any
			// earlier transport failure; report it, not the stale error.
			lastErr = nil
			lastHealth, lastRun, observed = health, run, true
		}
		if time.Now().After(deadline) {
			if observed {
				return bus.Health{}, bus.RunFile{}, fmt.Errorf("daemon did not reach the expected state in time (last observed version %q pid %d)", lastHealth.RuntimeVersion, lastRun.PID)
			}
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
		return "", nil, errServiceUnsupported
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
// daemon unless it is provably already on this build, and verifies the
// replacement process before reporting success. Managed-service state (the
// unit, the installed bytes, the pending marker) drives the restart decision;
// HTTP readiness never gates activation, so an unhealthy managed daemon is
// restarted rather than mistaken for absent or current.
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
	hadPending := false
	hadProcess := false
	wasHealthy := false
	var beforeHealth bus.Health
	var beforeRun bus.RunFile
	if action == "install" {
		if run, rerr := bus.ReadRunFile(paths.RunFile); rerr == nil {
			hadProcess = true
			beforeRun = run
		}
		if health, _, herr := probeDaemon(paths); herr == nil {
			wasHealthy = true
			beforeHealth = health
		}
		// Validate platform support before recording pending state, so an
		// unsupported platform never leaves a pending marker behind.
		if _, _, err := serviceFile(platform, home, stableBinaryPath(paths), paths); err != nil {
			return err
		}
		hadPending = pendingServiceActivation(paths)
		if err := markServiceActivationPending(paths, localVersion); err != nil {
			return err
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
			if err := run("--user", "daemon-reload"); err != nil {
				return err
			}
		}
		clearServiceActivationPending(paths)
		return nil
	}
	unitChanged := !bytes.Equal(old, body)
	if platform == "darwin" && !binaryChanged && !unitChanged && !hadPending && wasHealthy && beforeHealth.RuntimeVersion == localVersion {
		// Positive evidence only: a healthy daemon on this build with no
		// pending activation. Re-probe so a daemon that fell over between
		// the probes falls through to reactivation instead of reading current.
		if current, _, err := probeDaemon(paths); err == nil && current.RuntimeVersion == localVersion {
			clearServiceActivationPending(paths)
			fmt.Fprintln(out, "Login service is already current; the running daemon was left untouched.")
			return nil
		}
	}
	if err = writeConfigFile(path, body); err != nil {
		return err
	}
	if platform == "linux" {
		return activateLinuxService(serviceActivation{
			run: run, unit: unit, paths: paths, localVersion: localVersion,
			binaryChanged: binaryChanged, unitChanged: unitChanged, hadPending: hadPending,
			hadProcess: hadProcess, wasHealthy: wasHealthy,
			beforeHealth: beforeHealth, beforeRun: beforeRun, out: out,
		})
	}
	if len(old) > 0 {
		_ = run("bootout", domain, path)
	}
	if err = run("bootstrap", domain, path); err != nil {
		return fmt.Errorf("service file saved but activation failed; run service install again after restoring your user service manager: %w", err)
	}
	return verifyServiceDaemon(paths, localVersion, hadProcess, beforeRun, "bootstrap", out)
}

// serviceActivation carries the managed-service state observed before a Linux
// activation: what changed on disk, whether a previous attempt left work
// pending, and what (if anything) answered before activation started.
type serviceActivation struct {
	run           func(...string) error
	unit          string
	paths         bus.DaemonPaths
	localVersion  string
	binaryChanged bool
	unitChanged   bool
	hadPending    bool
	hadProcess    bool
	wasHealthy    bool
	beforeHealth  bus.Health
	beforeRun     bus.RunFile
	out           io.Writer
}

// activateLinuxService reloads user systemd, enables the unit, and restarts
// unless the managed daemon is provably already on this build. Readiness never
// gates activation: an unhealthy or unreachable managed daemon is restarted
// (restart also starts an inactive unit), never skipped. Every restart is
// followed by verification of the replacement process.
func activateLinuxService(activation serviceActivation) error {
	run, unit, paths := activation.run, activation.unit, activation.paths
	localVersion, out := activation.localVersion, activation.out
	if err := run("--user", "daemon-reload"); err != nil {
		return fmt.Errorf("service file saved but activation failed; run service install again after restoring your user service manager: %w", err)
	}
	// Plain enable: the conditional restart below starts an inactive unit,
	// so --now would start a stopped service twice.
	if err := run("--user", "enable", unit); err != nil {
		return fmt.Errorf("service file saved but activation failed; run service install again after restoring your user service manager: %w", err)
	}
	if !activation.binaryChanged && !activation.unitChanged && !activation.hadPending && activation.wasHealthy && activation.beforeHealth.RuntimeVersion == localVersion {
		// Positive evidence only. Re-probe so a daemon that fell over
		// between the probes falls through to restart instead of reading
		// current.
		if current, _, err := probeDaemon(paths); err == nil && current.RuntimeVersion == localVersion {
			clearServiceActivationPending(paths)
			fmt.Fprintln(out, "Login service is already current; the running daemon was left untouched.")
			return nil
		}
	}
	if err := run("--user", "restart", unit); err != nil {
		return fmt.Errorf("service file saved but restarting the login service failed; the running daemon may still be on the previous build: %w", err)
	}
	return verifyServiceDaemon(paths, localVersion, activation.hadProcess, activation.beforeRun, "restart", out)
}

// managedDaemonNeedsUpgrade reports whether a managed login-service upgrade
// must run before normal installation proceeds. Platforms without automatic
// login-service support and installations without a managed unit are not
// applicable: manually started daemons keep the existing reuse path. A managed
// installation whose daemon is unreachable, reports another version, differs
// in installed bytes or unit, or has a pending activation is stale and must
// activate; only a positively current managed daemon is reused. Genuine
// inspection errors are returned so the caller fails loudly instead of
// silently reusing a daemon it could not evaluate.
func managedDaemonNeedsUpgrade(paths bus.DaemonPaths) (bool, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return false, err
	}
	binary, err := os.Executable()
	if err != nil {
		return false, err
	}
	path, expected, err := serviceFile(runtime.GOOS, home, stableBinaryPath(paths), paths)
	if err != nil {
		if errors.Is(err, errServiceUnsupported) {
			return false, nil
		}
		return false, err
	}
	owned, err := readConfigFile(path)
	if err != nil {
		return false, err
	}
	if len(owned) == 0 || !bytes.Contains(owned, []byte("Managed by Agent Intercom")) {
		return false, nil
	}
	if pendingServiceActivation(paths) {
		return true, nil
	}
	health, _, err := probeDaemon(paths)
	if err != nil {
		return true, nil
	}
	if health.RuntimeVersion != bus.Version {
		return true, nil
	}
	if !bytes.Equal(owned, expected) {
		return true, nil
	}
	return !sameExecutableBytes(binary, stableBinaryPath(paths)), nil
}

// verifyServiceDaemon waits for the daemon to answer on the new build after a
// restart, requiring a new process identity when a process existed before. It
// reports failures instead of letting a stale or missing daemon read as success.
func verifyServiceDaemon(paths bus.DaemonPaths, localVersion string, hadProcess bool, beforeRun bus.RunFile, operation string, out io.Writer) error {
	health, run, err := awaitServiceDaemon(paths, serviceActivationTimeout, func(next bus.Health, nextRun bus.RunFile) bool {
		if next.RuntimeVersion != localVersion {
			return false
		}
		return !hadProcess || nextRun.PID != beforeRun.PID || nextRun.StartedAt != beforeRun.StartedAt
	})
	if err != nil {
		return fmt.Errorf("service file saved but the daemon did not come up on the new build after %s; check service status and run service install again: %w", operation, err)
	}
	clearServiceActivationPending(paths)
	if hadProcess {
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
