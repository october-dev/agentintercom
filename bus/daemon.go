package bus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type DaemonPaths struct {
	DataDir    string
	RuntimeDir string
	Database   string
	RunFile    string
	LockFile   string
}

func DefaultDaemonPaths() (DaemonPaths, error) {
	dataRoot := os.Getenv("OCTOBER_BUS_DATA_DIR")
	if dataRoot == "" {
		if runtime.GOOS == "windows" {
			dataRoot = os.Getenv("LOCALAPPDATA")
			if dataRoot == "" {
				var err error
				dataRoot, err = os.UserConfigDir()
				if err != nil {
					return DaemonPaths{}, err
				}
			}
			dataRoot = filepath.Join(dataRoot, "October Bus")
		} else {
			dataRoot = os.Getenv("XDG_DATA_HOME")
			if dataRoot == "" {
				home, err := os.UserHomeDir()
				if err != nil {
					return DaemonPaths{}, err
				}
				dataRoot = filepath.Join(home, ".local", "share")
			}
			dataRoot = filepath.Join(dataRoot, "october-bus")
		}
	}
	runtimeRoot := os.Getenv("OCTOBER_BUS_RUNTIME_DIR")
	if runtimeRoot == "" {
		if runtime.GOOS != "windows" && os.Getenv("XDG_RUNTIME_DIR") != "" {
			runtimeRoot = filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "october-bus")
		} else {
			name := "user"
			if current, err := user.Current(); err == nil {
				name = current.Uid
			}
			name = strings.NewReplacer("/", "_", `\`, "_", ":", "_").Replace(name)
			runtimeRoot = filepath.Join(os.TempDir(), "october-bus-"+name)
		}
	}
	return DaemonPaths{
		DataDir: dataRoot, RuntimeDir: runtimeRoot,
		Database: filepath.Join(dataRoot, "bus.db"),
		RunFile:  filepath.Join(runtimeRoot, "bus.json"),
		LockFile: filepath.Join(runtimeRoot, "bus.lock"),
	}, nil
}

func ReadRunFile(path string) (RunFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return RunFile{}, err
	}
	var run RunFile
	if err := json.Unmarshal(data, &run); err != nil {
		return RunFile{}, err
	}
	parsed, err := url.Parse(run.Address)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || run.ProtocolVersion != ProtocolVersion || run.PID < 1 || run.StartedAt == "" || len(run.AdminToken) != 43 {
		return RunFile{}, errors.New("Agent Intercom run file is invalid")
	}
	return run, nil
}

func writeRunFile(path string, run RunFile) error {
	data, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temporary := path + "." + strconv.Itoa(os.Getpid()) + ".tmp"
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	clean := func() { _ = os.Remove(temporary) }
	if _, err := file.Write(data); err != nil {
		file.Close()
		clean()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		clean()
		return err
	}
	if err := file.Close(); err != nil {
		clean()
		return err
	}
	if err := os.Chmod(temporary, 0o600); err != nil {
		clean()
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		clean()
		return err
	}
	return nil
}

func secureDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%s must be a real directory", path)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o700); err != nil {
			return err
		}
		info, err = os.Stat(path)
		if err != nil {
			return err
		}
		if info.Mode().Perm() != 0o700 {
			return fmt.Errorf("%s must have owner-only permissions", path)
		}
	}
	return nil
}

func acquireLock(paths DaemonPaths) (*os.File, error) {
	if err := secureDirectory(paths.RuntimeDir); err != nil {
		return nil, err
	}
	file, err := acquireFileLock(paths.LockFile)
	if err != nil {
		return nil, err
	}
	// Older releases did not hold an OS lock. A slow readiness check must not
	// let an upgrade take ownership from a live legacy daemon.
	if run, err := ReadRunFile(paths.RunFile); err == nil && processMayBeAlive(run.PID) {
		file.Close()
		return nil, errors.New("Agent Intercom discovery names a live process; stop it before starting another daemon")
	}
	return file, nil
}

type RunningDaemon struct {
	Server  *Server
	RunFile RunFile
	Paths   DaemonPaths
	lock    *os.File
	once    sync.Once
}

func StartDaemon(ctx context.Context, port int, paths *DaemonPaths) (*RunningDaemon, error) {
	resolved := DaemonPaths{}
	var err error
	if paths == nil {
		resolved, err = DefaultDaemonPaths()
		if err != nil {
			return nil, err
		}
	} else {
		resolved = *paths
	}
	if err := secureDirectory(resolved.DataDir); err != nil {
		return nil, err
	}
	lock, err := acquireLock(resolved)
	if err != nil {
		return nil, err
	}
	cleanup := func() {
		_ = os.Remove(resolved.RunFile)
		_ = lock.Close()
	}
	runtimeOptions, err := runtimeOptionsFromEnvironment()
	if err != nil {
		cleanup()
		return nil, err
	}
	runtimeValue, err := OpenWithOptions(resolved.Database, runtimeOptions)
	if err != nil {
		cleanup()
		return nil, err
	}
	adminToken, err := randomValue(32)
	if err != nil {
		runtimeValue.Close()
		cleanup()
		return nil, err
	}
	startedAt := time.Now().UTC().Format(time.RFC3339Nano)
	allowedOrigins := []string{}
	for _, origin := range strings.Split(os.Getenv("OCTOBER_BUS_ALLOWED_ORIGINS"), ",") {
		if origin = strings.TrimSpace(origin); origin != "" {
			allowedOrigins = append(allowedOrigins, origin)
		}
	}
	allowedHosts := []string{}
	for _, host := range strings.Split(os.Getenv("OCTOBER_BUS_ALLOWED_HOSTS"), ",") {
		if host = strings.TrimSpace(host); host != "" {
			allowedHosts = append(allowedHosts, host)
		}
	}
	server := NewServer(runtimeValue, ServerOptions{Port: port, AdminToken: adminToken, StartedAt: startedAt, AllowedOrigins: unique(allowedOrigins), AllowedHosts: unique(allowedHosts)})
	address, err := server.Start()
	if err != nil {
		runtimeValue.Close()
		cleanup()
		return nil, err
	}
	run := RunFile{ProtocolVersion: ProtocolVersion, Address: address, PID: os.Getpid(), StartedAt: startedAt, AdminToken: adminToken}
	if err := writeRunFile(resolved.RunFile, run); err != nil {
		stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		_ = server.Stop(stopCtx)
		cleanup()
		return nil, err
	}
	return &RunningDaemon{Server: server, RunFile: run, Paths: resolved, lock: lock}, nil
}

func runtimeOptionsFromEnvironment() (RuntimeOptions, error) {
	messageLimit, err := positiveEnvironmentInt64("OCTOBER_BUS_A2A_PRINCIPAL_MESSAGE_LIMIT")
	if err != nil {
		return RuntimeOptions{}, err
	}
	byteLimit, err := positiveEnvironmentInt64("OCTOBER_BUS_A2A_PRINCIPAL_BYTE_LIMIT")
	if err != nil {
		return RuntimeOptions{}, err
	}
	return RuntimeOptions{A2APrincipalMessageLimit: messageLimit, A2APrincipalByteLimit: byteLimit}, nil
}

func positiveEnvironmentInt64(name string) (int64, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 1 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return value, nil
}

func (d *RunningDaemon) Stop(ctx context.Context) error {
	var result error
	d.once.Do(func() {
		result = d.Server.Stop(ctx)
		_ = os.Remove(d.Paths.RunFile)
		_ = d.lock.Close()
	})
	return result
}
