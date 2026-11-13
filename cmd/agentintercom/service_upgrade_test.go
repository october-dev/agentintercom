package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/october-dev/agentintercom/bus"
)

const (
	upgradeVersionA = "9.9.9-A"
	upgradeVersionB = "9.9.9-B"
	upgradeToken    = "0123456789012345678901234567890123456789012"
	upgradeBefore   = "2026-01-01T00:00:00Z"
	upgradeAfter    = "2026-01-02T00:00:00Z"
)

type fakeDaemon struct {
	root        string
	home        string
	paths       bus.DaemonPaths
	server      *httptest.Server
	versionFile string
	runFile     string
	logFile     string
}

func setupFakeDaemon(t *testing.T, manager string) *fakeDaemon {
	t.Helper()
	if os.PathSeparator == '\\' {
		t.Skip("Unix service manager fakes")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	data := filepath.Join(root, "data")
	runtimeDir := filepath.Join(root, "run")
	t.Setenv("HOME", home)
	t.Setenv("OCTOBER_BUS_DATA_DIR", data)
	t.Setenv("OCTOBER_BUS_RUNTIME_DIR", runtimeDir)
	requireNoError(t, os.MkdirAll(runtimeDir, 0700))

	paths, err := bus.DefaultDaemonPaths()
	requireNoError(t, err)
	fake := &fakeDaemon{root: root, home: home, paths: paths, versionFile: filepath.Join(root, "version"), runFile: paths.RunFile, logFile: filepath.Join(root, manager+".log")}
	requireNoError(t, os.WriteFile(fake.versionFile, []byte(upgradeVersionA), 0600))
	fake.server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		version, err := os.ReadFile(fake.versionFile)
		if err != nil {
			http.Error(response, "no version", http.StatusInternalServerError)
			return
		}
		if os.Getenv("FAKE_503_UNTIL_FLIP") == "1" && string(version) != os.Getenv("FAKE_AFTER_VERSION") {
			response.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(response).Encode(map[string]string{
				"name": "october-bus", "protocolVersion": bus.ProtocolVersion,
				"runtimeVersion": string(version), "status": "unavailable", "startedAt": upgradeBefore,
			})
			return
		}
		_ = json.NewEncoder(response).Encode(map[string]string{
			"name": "october-bus", "protocolVersion": bus.ProtocolVersion,
			"runtimeVersion": string(version), "status": "ready", "startedAt": upgradeBefore,
		})
	}))
	t.Cleanup(fake.server.Close)

	bin := filepath.Join(root, "bin")
	requireNoError(t, os.MkdirAll(bin, 0700))
	script := bin + "/" + manager
	var body string
	if manager == "systemctl" {
		body = "#!/bin/sh\necho \"$@\" >> \"$FAKE_SYSTEMCTL_LOG\"\n" +
			"case \"$*\" in\n" +
			"  *\"--user restart \"*)\n" +
			"    [ \"$FAKE_SYSTEMCTL_MODE\" = \"fail-restart\" ] && exit 1\n" +
			"    [ \"$FAKE_SYSTEMCTL_MODE\" = \"no-flip\" ] && exit 0\n" +
			"    printf '%s' \"$FAKE_AFTER_VERSION\" > \"$FAKE_VERSION_FILE\"\n" +
			"    printf '{\"protocolVersion\":\"0.1\",\"address\":\"%s\",\"pid\":2000,\"startedAt\":\"" + upgradeAfter + "\",\"adminToken\":\"" + upgradeToken + "\"}' \"$FAKE_ADDRESS\" > \"$FAKE_RUN_FILE\"\n" +
			"    ;;\n" +
			"  *\"--user enable --now \"*)\n" +
			"    [ \"$FAKE_ENABLE_STARTS\" = \"1\" ] && {\n" +
			"      printf '%s' \"$FAKE_AFTER_VERSION\" > \"$FAKE_VERSION_FILE\"\n" +
			"      printf '{\"protocolVersion\":\"0.1\",\"address\":\"%s\",\"pid\":2000,\"startedAt\":\"" + upgradeAfter + "\",\"adminToken\":\"" + upgradeToken + "\"}' \"$FAKE_ADDRESS\" > \"$FAKE_RUN_FILE\"\n" +
			"    }\n" +
			"    ;;\n" +
			"esac\nexit 0\n"
	} else {
		body = "#!/bin/sh\necho \"$@\" >> \"$FAKE_SYSTEMCTL_LOG\"\n" +
			"case \"$1\" in\n" +
			"  bootstrap)\n" +
			"    [ \"$FAKE_LAUNCHCTL_MODE\" = \"fail-bootstrap\" ] && exit 1\n" +
			"    printf '%s' \"$FAKE_AFTER_VERSION\" > \"$FAKE_VERSION_FILE\"\n" +
			"    printf '{\"protocolVersion\":\"0.1\",\"address\":\"%s\",\"pid\":2000,\"startedAt\":\"" + upgradeAfter + "\",\"adminToken\":\"" + upgradeToken + "\"}' \"$FAKE_ADDRESS\" > \"$FAKE_RUN_FILE\"\n" +
			"    ;;\n" +
			"esac\nexit 0\n"
	}
	requireNoError(t, os.WriteFile(script, []byte(body), 0700))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_SYSTEMCTL_LOG", fake.logFile)
	t.Setenv("FAKE_VERSION_FILE", fake.versionFile)
	t.Setenv("FAKE_RUN_FILE", fake.runFile)
	t.Setenv("FAKE_ADDRESS", fake.server.URL)
	t.Setenv("FAKE_AFTER_VERSION", upgradeVersionB)
	return fake
}

func (f *fakeDaemon) writeDeadRunFile(t *testing.T) {
	t.Helper()
	// A run file naming a process that answers nothing: the daemon exists
	// (or did) but readiness fails, so activation must restart, not skip.
	data, err := json.Marshal(bus.RunFile{
		ProtocolVersion: bus.ProtocolVersion, Address: "http://127.0.0.1:1",
		PID: 1000, StartedAt: upgradeBefore, AdminToken: upgradeToken,
	})
	requireNoError(t, err)
	requireNoError(t, os.WriteFile(f.runFile, append(data, '\n'), 0600))
}

func (f *fakeDaemon) writeRunFile(t *testing.T, pid int, startedAt string) {
	t.Helper()
	data, err := json.Marshal(bus.RunFile{
		ProtocolVersion: bus.ProtocolVersion, Address: f.server.URL,
		PID: pid, StartedAt: startedAt, AdminToken: upgradeToken,
	})
	requireNoError(t, err)
	requireNoError(t, os.WriteFile(f.runFile, append(data, '\n'), 0600))
}

func (f *fakeDaemon) installCurrentBinary(t *testing.T) string {
	t.Helper()
	source, err := os.Executable()
	requireNoError(t, err)
	dest := stableBinaryPath(f.paths)
	requireNoError(t, os.MkdirAll(filepath.Dir(dest), 0700))
	content, err := os.ReadFile(source)
	requireNoError(t, err)
	requireNoError(t, os.WriteFile(dest, content, 0700))
	return dest
}

func (f *fakeDaemon) writeCurrentUnit(t *testing.T, platform, binary string) {
	t.Helper()
	path, body, err := serviceFile(platform, f.home, binary, f.paths)
	requireNoError(t, err)
	requireNoError(t, writeConfigFile(path, body))
}

func (f *fakeDaemon) managerLog(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(f.logFile)
	if os.IsNotExist(err) {
		return ""
	}
	requireNoError(t, err)
	return string(data)
}

func TestLinuxServiceInstallRestartsStaleDaemon(t *testing.T) {
	fake := setupFakeDaemon(t, "systemctl")
	fake.writeRunFile(t, 1000, upgradeBefore)
	// The installed bytes and unit already match; only the running version is stale.
	fake.writeCurrentUnit(t, "linux", fake.installCurrentBinary(t))

	var out bytes.Buffer
	requireNoError(t, manageServiceOn("linux", "install", upgradeVersionB, &out))
	log := fake.managerLog(t)
	require(t, strings.Contains(log, "--user daemon-reload"), "missing daemon-reload: %q", log)
	require(t, strings.Contains(log, "--user enable --now"), "missing enable --now: %q", log)
	require(t, strings.Contains(log, "--user restart"), "stale daemon was not restarted: %q", log)
	require(t, strings.Contains(out.String(), "upgraded and restarted"), "missing upgrade report: %q", out.String())

	version, err := os.ReadFile(fake.versionFile)
	requireNoError(t, err)
	require(t, string(version) == upgradeVersionB, "daemon still on %q", version)
	run, err := bus.ReadRunFile(fake.runFile)
	requireNoError(t, err)
	require(t, run.PID == 2000 && run.StartedAt == upgradeAfter, "run file still points at the old process: %#v", run)
	health, _, err := probeDaemon(fake.paths)
	requireNoError(t, err)
	require(t, health.RuntimeVersion == upgradeVersionB, "health still reports %q", health.RuntimeVersion)
}

func TestLinuxServiceInstallRestartsWhenBytesDiffer(t *testing.T) {
	fake := setupFakeDaemon(t, "systemctl")
	fake.writeRunFile(t, 1000, upgradeBefore)
	requireNoError(t, os.WriteFile(fake.versionFile, []byte(upgradeVersionB), 0600))
	// Same reported version (for example two dev builds), but the installed
	// copy does not match this executable, so the daemon must still restart.
	dest := stableBinaryPath(fake.paths)
	requireNoError(t, os.MkdirAll(filepath.Dir(dest), 0700))
	requireNoError(t, os.WriteFile(dest, []byte("older build"), 0700))
	fake.writeCurrentUnit(t, "linux", dest)

	var out bytes.Buffer
	requireNoError(t, manageServiceOn("linux", "install", upgradeVersionB, &out))
	require(t, strings.Contains(fake.managerLog(t), "--user restart"), "byte change did not restart: %q", out.String())
	require(t, strings.Contains(out.String(), "upgraded and restarted"), "missing upgrade report: %q", out.String())
}

func TestLinuxServiceReinstallWithoutChangesSkipsRestart(t *testing.T) {
	fake := setupFakeDaemon(t, "systemctl")
	fake.writeRunFile(t, 1000, upgradeBefore)
	requireNoError(t, os.WriteFile(fake.versionFile, []byte(upgradeVersionB), 0600))
	fake.writeCurrentUnit(t, "linux", fake.installCurrentBinary(t))

	var out bytes.Buffer
	requireNoError(t, manageServiceOn("linux", "install", upgradeVersionB, &out))
	log := fake.managerLog(t)
	require(t, strings.Contains(log, "--user enable --now"), "missing enable --now: %q", log)
	require(t, !strings.Contains(log, "--user restart"), "unchanged reinstall restarted: %q", log)
	require(t, strings.Contains(out.String(), "already current"), "missing current report: %q", out.String())
	run, err := bus.ReadRunFile(fake.runFile)
	requireNoError(t, err)
	require(t, run.PID == 1000 && run.StartedAt == upgradeBefore, "reinstall touched the running daemon: %#v", run)
}

func TestLinuxServiceInstallReportsRestartFailure(t *testing.T) {
	fake := setupFakeDaemon(t, "systemctl")
	fake.writeRunFile(t, 1000, upgradeBefore)
	fake.writeCurrentUnit(t, "linux", fake.installCurrentBinary(t))
	t.Setenv("FAKE_SYSTEMCTL_MODE", "fail-restart")

	var out bytes.Buffer
	err := manageServiceOn("linux", "install", upgradeVersionB, &out)
	require(t, err != nil && strings.Contains(err.Error(), "restarting the login service failed"), "restart failure error = %v", err)
	require(t, !strings.Contains(out.String(), "upgraded"), "failure reported as success: %q", out.String())
	require(t, pendingServiceActivation(fake.paths), "failed activation left no pending evidence for retry")
}

func TestLinuxServiceRetryAfterFailedSameVersionActivation(t *testing.T) {
	fake := setupFakeDaemon(t, "systemctl")
	fake.writeRunFile(t, 1000, upgradeBefore)
	requireNoError(t, os.WriteFile(fake.versionFile, []byte(upgradeVersionB), 0600))
	// Same reported version with different installed bytes; activation fails
	// after the stable copy is written.
	dest := stableBinaryPath(fake.paths)
	requireNoError(t, os.MkdirAll(filepath.Dir(dest), 0700))
	requireNoError(t, os.WriteFile(dest, []byte("older build"), 0700))
	fake.writeCurrentUnit(t, "linux", dest)
	t.Setenv("FAKE_SYSTEMCTL_MODE", "fail-restart")

	var first bytes.Buffer
	err := manageServiceOn("linux", "install", upgradeVersionB, &first)
	require(t, err != nil, "failed activation returned success: %q", first.String())

	// Retry with a working manager: files and versions now match, but the
	// pending activation must still restart and replace the old process.
	t.Setenv("FAKE_SYSTEMCTL_MODE", "")
	var second bytes.Buffer
	requireNoError(t, manageServiceOn("linux", "install", upgradeVersionB, &second))
	require(t, strings.Count(fake.managerLog(t), "--user restart") == 2, "retry did not restart: %q", fake.managerLog(t))
	require(t, !strings.Contains(second.String(), "already current"), "retry falsely reported current: %q", second.String())
	require(t, strings.Contains(second.String(), "upgraded and restarted"), "missing upgrade report: %q", second.String())
	run, err := bus.ReadRunFile(fake.runFile)
	requireNoError(t, err)
	require(t, run.PID == 2000 && run.StartedAt == upgradeAfter, "retry left the old process: %#v", run)
	require(t, !pendingServiceActivation(fake.paths), "successful retry left a pending marker")
}

func TestLinuxServiceRecoversUnreachableManagedDaemon(t *testing.T) {
	fake := setupFakeDaemon(t, "systemctl")
	fake.writeDeadRunFile(t)
	requireNoError(t, os.WriteFile(fake.versionFile, []byte(upgradeVersionB), 0600))
	fake.writeCurrentUnit(t, "linux", fake.installCurrentBinary(t))

	// The first probe fails, but the managed daemon must be restarted and
	// verified, never skipped as absent.
	var out bytes.Buffer
	requireNoError(t, manageServiceOn("linux", "install", upgradeVersionB, &out))
	require(t, strings.Contains(fake.managerLog(t), "--user restart"), "unreachable daemon was not restarted: %q", out.String())
	require(t, strings.Contains(out.String(), "upgraded and restarted"), "missing upgrade report: %q", out.String())
	run, err := bus.ReadRunFile(fake.runFile)
	requireNoError(t, err)
	require(t, run.PID == 2000 && run.StartedAt == upgradeAfter, "run file still points at the dead process: %#v", run)
	health, _, err := probeDaemon(fake.paths)
	requireNoError(t, err)
	require(t, health.RuntimeVersion == upgradeVersionB, "health still reports %q", health.RuntimeVersion)
}

func TestLinuxServiceRecoversDaemonReturning503(t *testing.T) {
	fake := setupFakeDaemon(t, "systemctl")
	fake.writeRunFile(t, 1000, upgradeBefore)
	fake.writeCurrentUnit(t, "linux", fake.installCurrentBinary(t))
	t.Setenv("FAKE_503_UNTIL_FLIP", "1")

	// The old daemon answers 503 until restart; readiness must not gate
	// activation or discard the known version change.
	var out bytes.Buffer
	requireNoError(t, manageServiceOn("linux", "install", upgradeVersionB, &out))
	require(t, strings.Contains(fake.managerLog(t), "--user restart"), "503 daemon was not restarted: %q", out.String())
	require(t, strings.Contains(out.String(), "upgraded and restarted"), "missing upgrade report: %q", out.String())
	health, _, err := probeDaemon(fake.paths)
	requireNoError(t, err)
	require(t, health.RuntimeVersion == upgradeVersionB, "health still reports %q", health.RuntimeVersion)
}

func TestLinuxServiceUnitOnlyChangeRestarts(t *testing.T) {
	fake := setupFakeDaemon(t, "systemctl")
	fake.writeRunFile(t, 1000, upgradeBefore)
	requireNoError(t, os.WriteFile(fake.versionFile, []byte(upgradeVersionB), 0600))
	fake.installCurrentBinary(t)
	// Bytes and version match, but the installed unit drifted.
	fake.writeCurrentUnit(t, "linux", filepath.Join(fake.root, "elsewhere", "agentintercom"))

	var out bytes.Buffer
	requireNoError(t, manageServiceOn("linux", "install", upgradeVersionB, &out))
	require(t, strings.Contains(fake.managerLog(t), "--user restart"), "unit change did not restart: %q", out.String())
	require(t, strings.Contains(out.String(), "upgraded and restarted"), "missing upgrade report: %q", out.String())
	run, err := bus.ReadRunFile(fake.runFile)
	requireNoError(t, err)
	require(t, run.PID == 2000 && run.StartedAt == upgradeAfter, "unit change left the old process: %#v", run)
}

func TestLinuxServiceInstallVerifiesNewVersionAfterRestart(t *testing.T) {
	fake := setupFakeDaemon(t, "systemctl")
	fake.writeRunFile(t, 1000, upgradeBefore)
	fake.writeCurrentUnit(t, "linux", fake.installCurrentBinary(t))
	t.Setenv("FAKE_SYSTEMCTL_MODE", "no-flip")
	old := serviceActivationTimeout
	serviceActivationTimeout = 2 * time.Second
	t.Cleanup(func() { serviceActivationTimeout = old })

	var out bytes.Buffer
	err := manageServiceOn("linux", "install", upgradeVersionB, &out)
	require(t, err != nil && strings.Contains(err.Error(), "did not come up on the new build"), "stale verification error = %v", err)
	require(t, !strings.Contains(out.String(), "upgraded"), "stale daemon reported as success: %q", out.String())
}

func TestLinuxServiceFreshInstallStartsDaemonThroughRestart(t *testing.T) {
	fake := setupFakeDaemon(t, "systemctl")
	t.Setenv("FAKE_ENABLE_STARTS", "1")

	// No daemon is provably current, so install restarts; restart also
	// starts an inactive unit, which both starts fresh daemons and recovers
	// unhealthy ones without gating on readiness first.
	var out bytes.Buffer
	requireNoError(t, manageServiceOn("linux", "install", upgradeVersionB, &out))
	log := fake.managerLog(t)
	require(t, strings.Contains(log, "--user enable --now"), "missing enable --now: %q", log)
	require(t, strings.Contains(log, "--user restart"), "fresh install skipped restart: %q", log)
	require(t, strings.Contains(out.String(), "Login service installed"), "missing install report: %q", out.String())
	health, _, err := probeDaemon(fake.paths)
	requireNoError(t, err)
	require(t, health.RuntimeVersion == upgradeVersionB, "fresh daemon reports %q", health.RuntimeVersion)
}

func TestDarwinServiceUpgradeRestartsStaleDaemon(t *testing.T) {
	fake := setupFakeDaemon(t, "launchctl")
	fake.writeRunFile(t, 1000, upgradeBefore)
	fake.writeCurrentUnit(t, "darwin", fake.installCurrentBinary(t))

	var out bytes.Buffer
	requireNoError(t, manageServiceOn("darwin", "install", upgradeVersionB, &out))
	log := fake.managerLog(t)
	require(t, strings.Contains(log, "bootout"), "missing bootout: %q", log)
	require(t, strings.Contains(log, "bootstrap"), "missing bootstrap: %q", log)
	require(t, strings.Contains(out.String(), "upgraded and restarted"), "missing upgrade report: %q", out.String())
	health, _, err := probeDaemon(fake.paths)
	requireNoError(t, err)
	require(t, health.RuntimeVersion == upgradeVersionB, "daemon still on %q", health.RuntimeVersion)
}

func TestDarwinServiceReinstallWithoutChangesTouchesNothing(t *testing.T) {
	fake := setupFakeDaemon(t, "launchctl")
	fake.writeRunFile(t, 1000, upgradeBefore)
	requireNoError(t, os.WriteFile(fake.versionFile, []byte(upgradeVersionB), 0600))
	fake.writeCurrentUnit(t, "darwin", fake.installCurrentBinary(t))

	var out bytes.Buffer
	requireNoError(t, manageServiceOn("darwin", "install", upgradeVersionB, &out))
	require(t, fake.managerLog(t) == "", "unchanged reinstall touched launchctl: %q", fake.managerLog(t))
	require(t, strings.Contains(out.String(), "already current"), "missing current report: %q", out.String())
}

func TestDarwinServiceBootstrapFailure(t *testing.T) {
	fake := setupFakeDaemon(t, "launchctl")
	fake.writeRunFile(t, 1000, upgradeBefore)
	fake.writeCurrentUnit(t, "darwin", fake.installCurrentBinary(t))
	t.Setenv("FAKE_LAUNCHCTL_MODE", "fail-bootstrap")

	var out bytes.Buffer
	err := manageServiceOn("darwin", "install", upgradeVersionB, &out)
	require(t, err != nil && strings.Contains(err.Error(), "activation failed"), "bootstrap failure error = %v", err)
	require(t, !strings.Contains(out.String(), "upgraded"), "failure reported as success: %q", out.String())
	require(t, pendingServiceActivation(fake.paths), "failed activation left no pending evidence for retry")
}

func TestDarwinServiceRetryAfterFailedSameVersionActivation(t *testing.T) {
	fake := setupFakeDaemon(t, "launchctl")
	fake.writeRunFile(t, 1000, upgradeBefore)
	requireNoError(t, os.WriteFile(fake.versionFile, []byte(upgradeVersionB), 0600))
	dest := stableBinaryPath(fake.paths)
	requireNoError(t, os.MkdirAll(filepath.Dir(dest), 0700))
	requireNoError(t, os.WriteFile(dest, []byte("older build"), 0700))
	fake.writeCurrentUnit(t, "darwin", dest)
	t.Setenv("FAKE_LAUNCHCTL_MODE", "fail-bootstrap")

	var first bytes.Buffer
	err := manageServiceOn("darwin", "install", upgradeVersionB, &first)
	require(t, err != nil, "failed activation returned success: %q", first.String())

	// Retry with a working manager: the pending activation must reactivate
	// even though files and versions now match.
	t.Setenv("FAKE_LAUNCHCTL_MODE", "")
	var second bytes.Buffer
	requireNoError(t, manageServiceOn("darwin", "install", upgradeVersionB, &second))
	require(t, strings.Count(fake.managerLog(t), "bootstrap") == 2, "retry did not reactivate: %q", fake.managerLog(t))
	require(t, !strings.Contains(second.String(), "already current"), "retry falsely reported current: %q", second.String())
	require(t, strings.Contains(second.String(), "upgraded and restarted"), "missing upgrade report: %q", second.String())
	run, err := bus.ReadRunFile(fake.runFile)
	requireNoError(t, err)
	require(t, run.PID == 2000 && run.StartedAt == upgradeAfter, "retry left the old process: %#v", run)
}

func TestInstallRecoversUnreachableManagedDaemon(t *testing.T) {
	manager := "systemctl"
	if runtime.GOOS == "darwin" {
		manager = "launchctl"
	}
	fake := setupFakeDaemon(t, manager)
	failEnv := "FAKE_SYSTEMCTL_MODE"
	failValue := "fail-restart"
	if manager == "launchctl" {
		failEnv = "FAKE_LAUNCHCTL_MODE"
		failValue = "fail-bootstrap"
	}
	// Saved workspace/app state with a managed daemon that answers nothing.
	t.Setenv("FAKE_AFTER_VERSION", bus.Version)
	requireNoError(t, os.WriteFile(fake.versionFile, []byte(bus.Version), 0600))
	fake.writeDeadRunFile(t)
	fake.writeCurrentUnit(t, runtime.GOOS, fake.installCurrentBinary(t))
	requireNoError(t, bus.SaveScopeToken(fake.paths.DataDir, "default", strings.Repeat("b", 43)))
	configPath := filepath.Join(fake.root, "codex-config.toml")
	requireNoError(t, os.WriteFile(configPath, []byte("# managed\n"), 0600))
	statePath, err := installState("codex")
	requireNoError(t, err)
	requireNoError(t, saveInstallState(statePath, []installedConfig{{Path: configPath, Before: nil, After: []byte("# managed\n")}}))
	stub := filepath.Join(fake.root, "bin", "codex")
	requireNoError(t, os.WriteFile(stub, []byte("#!/bin/sh\nexit 0\n"), 0700))

	// A failed activation must fail the whole install, never print success.
	t.Setenv(failEnv, failValue)
	var failed bytes.Buffer
	err = installCommand([]string{"--only", "codex"}, &failed)
	require(t, err != nil, "failed managed upgrade returned success: %q", failed.String())
	require(t, !strings.Contains(failed.String(), "Reconnect MCP"), "failed install printed success: %q", failed.String())

	// With a working manager the same command restarts, verifies the
	// replacement, and completes the install.
	t.Setenv(failEnv, "")
	var out bytes.Buffer
	requireNoError(t, installCommand([]string{"--only", "codex"}, &out))
	require(t, strings.Contains(out.String(), "upgraded and restarted"), "missing upgrade report: %q", out.String())
	require(t, strings.Contains(out.String(), "Reconnect MCP"), "missing install success: %q", out.String())
	run, err := bus.ReadRunFile(fake.runFile)
	requireNoError(t, err)
	require(t, run.PID == 2000 && run.StartedAt == upgradeAfter, "install left the dead process: %#v", run)
}

func TestServicePendingMarkerRoundTrip(t *testing.T) {
	paths := bus.DaemonPaths{DataDir: t.TempDir(), RuntimeDir: t.TempDir()}
	require(t, !pendingServiceActivation(paths), "fresh paths report pending")
	requireNoError(t, markServiceActivationPending(paths, "9.9.9-test"))
	require(t, pendingServiceActivation(paths), "marker was not recorded")
	clearServiceActivationPending(paths)
	require(t, !pendingServiceActivation(paths), "marker was not cleared")
}

func TestStableExecutableSkipsUnchangedCopy(t *testing.T) {
	root := t.TempDir()
	t.Setenv("OCTOBER_BUS_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("OCTOBER_BUS_RUNTIME_DIR", filepath.Join(root, "run"))

	first, changed, err := installStableExecutable()
	requireNoError(t, err)
	require(t, changed, "fresh install reported no change")
	before, err := os.ReadFile(first)
	requireNoError(t, err)
	second, changed, err := installStableExecutable()
	requireNoError(t, err)
	require(t, !changed && second == first, "unchanged reinstall recopied: %q changed=%v", second, changed)
	after, err := os.ReadFile(second)
	requireNoError(t, err)
	require(t, bytes.Equal(before, after), "unchanged reinstall rewrote bytes")
	require(t, !sameExecutableBytes(first, filepath.Join(root, "missing")), "missing dest compared equal")
}

func TestManagedDaemonNeedsUpgrade(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("Unix service paths")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	t.Setenv("HOME", home)
	t.Setenv("OCTOBER_BUS_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("OCTOBER_BUS_RUNTIME_DIR", filepath.Join(root, "run"))
	paths, err := bus.DefaultDaemonPaths()
	requireNoError(t, err)
	requireNoError(t, os.MkdirAll(paths.RuntimeDir, 0700))

	// No managed service file: a manually started daemon is reused, not upgraded.
	stale, err := managedDaemonNeedsUpgrade(paths)
	requireNoError(t, err)
	require(t, !stale, "manual daemon flagged for upgrade")

	versionFile := filepath.Join(root, "version")
	requireNoError(t, os.WriteFile(versionFile, []byte(bus.Version), 0600))
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		version, _ := os.ReadFile(versionFile)
		_ = json.NewEncoder(response).Encode(map[string]string{"runtimeVersion": string(version)})
	}))
	defer server.Close()
	data, err := json.Marshal(bus.RunFile{
		ProtocolVersion: bus.ProtocolVersion, Address: server.URL,
		PID: 1000, StartedAt: upgradeBefore, AdminToken: upgradeToken,
	})
	requireNoError(t, err)
	requireNoError(t, os.WriteFile(paths.RunFile, append(data, '\n'), 0600))

	// An unmanaged service file is also left alone.
	source, err := os.Executable()
	requireNoError(t, err)
	unmanagedPath, _, err := serviceFile(runtime.GOOS, home, source, paths)
	requireNoError(t, err)
	requireNoError(t, writeConfigFile(unmanagedPath, []byte("# foreign unit\n")))
	stale, err = managedDaemonNeedsUpgrade(paths)
	requireNoError(t, err)
	require(t, !stale, "unmanaged service flagged for upgrade")

	// The managed unit references the stable executable path, as a real
	// service install writes it.
	dest := stableBinaryPath(paths)
	managedPath, managedBody, err := serviceFile(runtime.GOOS, home, dest, paths)
	requireNoError(t, err)
	require(t, managedPath == unmanagedPath, "service file moved: %q", managedPath)
	requireNoError(t, writeConfigFile(managedPath, managedBody))

	// Current version but different installed bytes: stale.
	stale, err = managedDaemonNeedsUpgrade(paths)
	requireNoError(t, err)
	require(t, stale, "byte-different daemon not flagged")

	// Identical bytes and version: current.
	requireNoError(t, os.MkdirAll(filepath.Dir(dest), 0700))
	content, err := os.ReadFile(source)
	requireNoError(t, err)
	requireNoError(t, os.WriteFile(dest, content, 0700))
	stale, err = managedDaemonNeedsUpgrade(paths)
	requireNoError(t, err)
	require(t, !stale, "current daemon flagged for upgrade")

	// A pending activation from a failed attempt: stale even though every
	// file and the reported version match.
	requireNoError(t, markServiceActivationPending(paths, bus.Version))
	stale, err = managedDaemonNeedsUpgrade(paths)
	requireNoError(t, err)
	require(t, stale, "pending activation not flagged")
	clearServiceActivationPending(paths)

	// Older reported version: stale even with identical bytes.
	requireNoError(t, os.WriteFile(versionFile, []byte("0.0.0-old"), 0600))
	stale, err = managedDaemonNeedsUpgrade(paths)
	requireNoError(t, err)
	require(t, stale, "older version not flagged")
	requireNoError(t, os.WriteFile(versionFile, []byte(bus.Version), 0600))

	// A managed daemon that answers nothing is stale: normal install must
	// activate (restart and verify) instead of silently reusing it.
	server.Close()
	stale, err = managedDaemonNeedsUpgrade(paths)
	requireNoError(t, err)
	require(t, stale, "unreachable managed daemon not flagged")
}
