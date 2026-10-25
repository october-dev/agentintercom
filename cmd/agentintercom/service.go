package main

import (
	"bytes"
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

	"github.com/october-dev/october-bus/bus"
)

const userServiceName = "dev.october.agent-intercom"

func stableExecutable() (string, error) {
	source, err := os.Executable()
	if err != nil {
		return "", err
	}
	paths, err := bus.DefaultDaemonPaths()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(paths.DataDir, "bin")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	if err := nativePrivateDirectory(dir); err != nil {
		return "", err
	}
	name := "agentintercom"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	dest := filepath.Join(dir, name)
	if filepath.Clean(source) == filepath.Clean(dest) {
		return dest, nil
	}
	in, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.CreateTemp(dir, ".binary-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(out.Name())
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return "", err
	}
	if err := out.Chmod(0700); err != nil {
		return "", err
	}
	if err := out.Sync(); err != nil {
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(out.Name(), dest); err != nil {
		return "", err
	}
	return dest, nil
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
	if action == "install" {
		binary, err = stableExecutable()
		if err != nil {
			return err
		}
	}
	path, body, err := serviceFile(runtime.GOOS, home, binary, paths)
	if err != nil {
		return err
	}
	manager := "systemctl"
	if runtime.GOOS == "darwin" {
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
		if runtime.GOOS == "linux" {
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
		if runtime.GOOS == "linux" {
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
		if runtime.GOOS == "linux" {
			return run("--user", "daemon-reload")
		}
		return nil
	}
	if err = writeConfigFile(path, body); err != nil {
		return err
	}
	if runtime.GOOS == "linux" {
		if err = run("--user", "daemon-reload"); err == nil {
			err = run("--user", "enable", "--now", unit)
		}
	} else {
		if len(old) > 0 {
			_ = run("bootout", domain, path)
		}
		err = run("bootstrap", domain, path)
	}
	if err != nil {
		return fmt.Errorf("service file saved but activation failed; run service install again after restoring your user service manager: %w", err)
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
