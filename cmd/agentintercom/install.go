package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/october-dev/agentintercom/bus"
)

const managedBegin = "# >>> Agent Intercom managed MCP >>>\n"
const managedEnd = "# <<< Agent Intercom managed MCP <<<\n"

type installedConfig struct {
	Path   string `json:"path"`
	Before []byte `json:"before"`
	After  []byte `json:"after"`
}

func readConfigFile(path string) ([]byte, error) {
	i, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !i.Mode().IsRegular() || i.Mode()&os.ModeSymlink != 0 || i.Size() > 4*1024*1024 {
		return nil, errors.New("configuration must be a regular file smaller than 4 MiB; symlinks are not edited")
	}
	return os.ReadFile(path)
}

func writeConfigFile(path string, data []byte) error {
	if _, err := readConfigFile(path); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".agent-intercom-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func installState(target string) (string, error) {
	p, err := bus.DefaultDaemonPaths()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(p.DataDir, "installation")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	if err = nativePrivateDirectory(dir); err != nil {
		return "", err
	}
	return filepath.Join(dir, target+".json"), nil
}

func saveInstallState(path string, records []installedConfig) error {
	data, err := json.Marshal(records)
	if err != nil {
		return err
	}
	return writeConfigFile(path, data)
}

func managedTOML(before, block []byte) ([]byte, error) {
	if bytes.Contains(before, []byte(managedBegin)) || bytes.Contains(before, []byte(managedEnd)) {
		return nil, errors.New("managed markers already exist without matching installation state; left unchanged")
	}
	// Quoted TOML table names must not bypass the unmanaged-entry check.
	normalized := strings.NewReplacer("\"", "", "'", "", " ", "", "\t", "").Replace(string(before))
	if regexp.MustCompile(`(?m)^\[mcp_servers\.agent_intercom(?:\]|\.)`).MatchString(normalized) || strings.Contains(normalized, "mcp_servers={") {
		return nil, errors.New("an unmanaged agent_intercom MCP entry exists; remove it yourself or use install --print")
	}
	inMCP := false
	for _, line := range strings.Split(normalized, "\n") {
		if strings.HasPrefix(line, "[") {
			if strings.Contains(line, "\\") {
				return nil, errors.New("escaped TOML table names require manual configuration; use --print")
			}
			inMCP = line == "[mcp_servers]"
		}
		if (inMCP && strings.HasPrefix(line, "agent_intercom=")) || strings.HasPrefix(line, "mcp_servers.agent_intercom") {
			return nil, errors.New("an unmanaged MCP entry exists; left unchanged")
		}
	}
	result := append([]byte(nil), before...)
	result = append(result, []byte("\n"+managedBegin)...)
	result = append(result, block...)
	if len(result) > 0 && result[len(result)-1] != '\n' {
		result = append(result, '\n')
	}
	result = append(result, []byte(managedEnd)...)
	return result, nil
}

func installCommand(args []string, out io.Writer) error {
	f := flag.NewFlagSet("install", flag.ContinueOnError)
	only := f.String("only", "", "claude or codex; omitted installs detected CLIs")
	workspace := f.String("workspace", "default", "local workspace")
	hosted := f.String("hosted", "", "saved hosted device profile")
	printOnly := f.Bool("print", false, "print configuration without changing app settings")
	noService := f.Bool("no-service", false, "use an already-running local daemon")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("install accepts flags only")
	}
	if *only != "" && *only != "claude" && *only != "codex" && !*printOnly {
		return errors.New("automatic install supports claude and codex; use --print for other MCP apps")
	}
	selection := []string{"--workspace", *workspace}
	if *hosted != "" {
		selection = []string{"--hosted", *hosted}
	}
	if *printOnly {
		if *only == "codex" {
			selection = append(selection, "--format", "toml")
		}
		return connectCommand(selection, out)
	}
	var targets []string
	for _, target := range []string{"claude", "codex"} {
		if *only != "" && *only != target {
			continue
		}
		if _, err := exec.LookPath(target); err == nil {
			targets = append(targets, target)
		} else if *only != "" {
			return fmt.Errorf("%s is not installed; use --print to get its configuration", target)
		}
	}
	if len(targets) == 0 {
		return errors.New("no supported CLI found; use install --print for your MCP app")
	}
	if *hosted == "" {
		p, err := bus.DefaultDaemonPaths()
		if err != nil {
			return err
		}
		if _, err = bus.ReadRunFile(p.RunFile); err != nil && !*noService {
			if err = manageService("install", out); err != nil {
				return err
			}
		}
		if err := ensureInstallWorkspace(*workspace); err != nil {
			return err
		}
	}
	binary, err := stableExecutable()
	if err != nil {
		return err
	}
	for _, target := range targets {
		if err := installTarget(target, binary, selection, out); err != nil {
			return err
		}
	}
	fmt.Fprintln(out, "Reconnect MCP in your app, or restart it. Ask it to list its peers. Existing messages stay in their workspace.")
	return nil
}

func installTarget(target, binary string, selection []string, out io.Writer) error {
	statePath, err := installState(target)
	if err != nil {
		return err
	}
	stateData, err := readConfigFile(statePath)
	if err != nil {
		return err
	}
	if len(stateData) > 0 {
		var records []installedConfig
		if json.Unmarshal(stateData, &records) != nil {
			return errors.New("installation state is invalid")
		}
		for _, r := range records {
			current, err := readConfigFile(r.Path)
			if err != nil {
				return err
			}
			if _, err := removeInstalledConfig(r, current); err != nil {
				return errors.New("managed configuration changed since installation; use uninstall or restore its managed entry before reinstalling")
			}
		}
		fmt.Fprintln(out, target+": already installed; use uninstall before selecting another workspace")
		return nil
	}
	var output bytes.Buffer
	args := append(append([]string(nil), selection...), "--command", binary, "--name", target, "--format", "entry")
	if err := connectCommand(args, &output); err != nil {
		return err
	}
	var entry stdioConfig
	if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
		return err
	}
	entry.Args = append(entry.Args, "--native", target)
	paths, err := bus.DefaultDaemonPaths()
	if err != nil {
		return err
	}
	if entry.Env == nil {
		entry.Env = map[string]string{}
	}
	entry.Env["OCTOBER_BUS_DATA_DIR"] = paths.DataDir
	entry.Env["OCTOBER_BUS_RUNTIME_DIR"] = paths.RuntimeDir
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	if target == "codex" {
		dir := os.Getenv("CODEX_HOME")
		if dir == "" {
			dir = filepath.Join(home, ".codex")
		}
		path := filepath.Join(dir, "config.toml")
		before, err := readConfigFile(path)
		if err != nil {
			return err
		}
		command, _ := json.Marshal(entry.Command)
		argv, _ := json.Marshal(entry.Args)
		envData, _ := json.Marshal(paths.DataDir)
		envRun, _ := json.Marshal(paths.RuntimeDir)
		block := []byte(fmt.Sprintf("[mcp_servers.agent_intercom]\ncommand = %s\nargs = %s\nstartup_timeout_sec = 20\ntool_timeout_sec = 60\ndefault_tools_approval_mode = \"prompt\"\nenv = { OCTOBER_BUS_DATA_DIR = %s, OCTOBER_BUS_RUNTIME_DIR = %s }\n", command, argv, envData, envRun))
		after, err := managedTOML(before, block)
		if err != nil {
			return err
		}
		if err = saveInstallState(statePath, []installedConfig{{path, before, after}}); err != nil {
			return err
		}
		if err = writeConfigFile(path, after); err != nil {
			return err
		}
	} else {
		// The CLI owns its user MCP file. Custom configuration roots can still use
		// --print; avoid guessing at a second user-config file location.
		if os.Getenv("CLAUDE_CONFIG_DIR") != "" {
			return errors.New("custom CLAUDE_CONFIG_DIR: use install --print and add the MCP entry with claude mcp add-json")
		}
		path := filepath.Join(home, ".claude.json")
		before, err := readConfigFile(path)
		if err != nil {
			return err
		}
		var config map[string]json.RawMessage
		if len(before) > 0 && json.Unmarshal(before, &config) != nil {
			return errors.New("Claude user configuration is invalid; left unchanged")
		}
		var servers map[string]json.RawMessage
		if raw := config["mcpServers"]; len(raw) > 0 && json.Unmarshal(raw, &servers) != nil {
			return errors.New("Claude MCP configuration is invalid; left unchanged")
		}
		if _, ok := servers["agent_intercom"]; ok {
			return errors.New("an unmanaged agent_intercom MCP entry exists; left unchanged")
		}
		hookPath := filepath.Join(home, ".claude", "settings.json")
		hookBefore, err := readConfigFile(hookPath)
		if err != nil {
			return err
		}
		hookAfter, err := claudeHooks(hookBefore, binary, paths)
		if err != nil {
			return err
		}
		// Save the original bytes before the supported CLI touches the file.
		records := []installedConfig{{Path: path, Before: before}, {hookPath, hookBefore, hookAfter}}
		if err = saveInstallState(statePath, records); err != nil {
			return err
		}
		data, _ := json.Marshal(entry)
		c := exec.Command("claude", "mcp", "add-json", "--scope", "user", "agent_intercom", string(data))
		c.Stdout, c.Stderr = io.Discard, io.Discard
		if err = c.Run(); err != nil {
			_ = os.Remove(statePath)
			return errors.New("Claude rejected the MCP configuration; inspect claude mcp list")
		}
		after, err := readConfigFile(path)
		if err != nil {
			return err
		}
		records[0].After = after
		if err = saveInstallState(statePath, records); err != nil {
			return err
		}
		if err = writeConfigFile(hookPath, hookAfter); err != nil {
			return err
		}
	}
	fmt.Fprintln(out, target+": MCP configuration installed; original configuration saved privately")
	return nil
}

func claudeHooks(before []byte, binary string, paths bus.DaemonPaths) ([]byte, error) {
	settings := map[string]any{}
	if len(before) > 0 && (json.Unmarshal(before, &settings) != nil || settings == nil) {
		return nil, errors.New("Claude settings are invalid; left unchanged")
	}
	hooks, ok := settings["hooks"].(map[string]any)
	if settings["hooks"] != nil && !ok {
		return nil, errors.New("Claude hooks must be an object")
	}
	if hooks == nil {
		hooks = map[string]any{}
	}
	command := "OCTOBER_BUS_DATA_DIR=" + shellArgument(paths.DataDir) + " OCTOBER_BUS_RUNTIME_DIR=" + shellArgument(paths.RuntimeDir) + " " + shellArgument(binary) + " session hook claude"
	if runtime.GOOS == "windows" {
		return nil, errors.New("automatic Claude socket hooks require macOS or Linux; use install --print for standard MCP on Windows")
	}
	for _, event := range []string{"SessionStart", "Stop", "SessionEnd"} {
		entries, ok := hooks[event].([]any)
		if hooks[event] != nil && !ok {
			return nil, errors.New("Claude hook event must be an array")
		}
		entries = append(entries, map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command, "timeout": 5}}})
		hooks[event] = entries
	}
	settings["hooks"] = hooks
	data, err := json.MarshalIndent(settings, "", "  ")
	return append(data, '\n'), err
}

func uninstallCommand(args []string, out io.Writer) error {
	f := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	only := f.String("only", "", "claude or codex; omitted removes both managed installations")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || (*only != "" && *only != "claude" && *only != "codex") {
		return errors.New("use uninstall [--only claude|codex]")
	}
	for _, target := range []string{"claude", "codex"} {
		if *only != "" && target != *only {
			continue
		}
		statePath, err := installState(target)
		if err != nil {
			return err
		}
		data, err := readConfigFile(statePath)
		if err != nil {
			return err
		}
		if len(data) == 0 {
			continue
		}
		var records []installedConfig
		if json.Unmarshal(data, &records) != nil {
			return errors.New("installation state is invalid; configuration left intact")
		}
		// Plan all removals before writing; preserve unrelated edits made by the
		// app after installation, including Claude's own user-file updates.
		results := make([][]byte, len(records))
		for i, r := range records {
			current, err := readConfigFile(r.Path)
			if err != nil {
				return err
			}
			results[i], err = removeInstalledConfig(r, current)
			if err != nil {
				return fmt.Errorf("%s: %w; private backup: %s", r.Path, err, statePath)
			}
		}
		for i, r := range records {
			if results[i] == nil {
				err = os.Remove(r.Path)
			} else {
				err = writeConfigFile(r.Path, results[i])
			}
			if err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		if err = os.Remove(statePath); err != nil {
			return err
		}
		fmt.Fprintln(out, target+": original configuration restored; workspace data retained")
	}
	if *only == "" && runtime.GOOS != "windows" {
		return manageService("uninstall", out)
	}
	return nil
}

func removeInstalledConfig(r installedConfig, current []byte) ([]byte, error) {
	if bytes.Equal(current, r.After) {
		return r.Before, nil
	}
	conflict := errors.New("managed entry was edited; remove only that entry manually, leaving other settings intact")
	if filepath.Ext(r.Path) == ".toml" {
		if !bytes.HasPrefix(r.After, r.Before) {
			return nil, conflict
		}
		added := r.After[len(r.Before):]
		if len(added) == 0 || bytes.Count(current, added) != 1 {
			return nil, conflict
		}
		return bytes.Replace(current, added, nil, 1), nil
	}
	var now, installed, original map[string]any
	if json.Unmarshal(current, &now) != nil || json.Unmarshal(r.After, &installed) != nil {
		return nil, conflict
	}
	if len(r.Before) > 0 && json.Unmarshal(r.Before, &original) != nil {
		return nil, conflict
	}
	equal := func(a, b any) bool { x, _ := json.Marshal(a); y, _ := json.Marshal(b); return bytes.Equal(x, y) }
	if filepath.Base(r.Path) == "settings.json" {
		nowHooks, _ := now["hooks"].(map[string]any)
		installedHooks, _ := installed["hooks"].(map[string]any)
		originalHooks, _ := original["hooks"].(map[string]any)
		for _, event := range []string{"SessionStart", "Stop", "SessionEnd"} {
			entries, _ := installedHooks[event].([]any)
			if len(entries) == 0 {
				return nil, conflict
			}
			owned := entries[len(entries)-1]
			currentEntries, ok := nowHooks[event].([]any)
			if !ok {
				return nil, conflict
			}
			filtered := make([]any, 0, len(currentEntries))
			count := 0
			for _, entry := range currentEntries {
				if equal(entry, owned) {
					count++
				} else {
					filtered = append(filtered, entry)
				}
			}
			if count != 1 {
				return nil, conflict
			}
			if len(filtered) == 0 && originalHooks[event] == nil {
				delete(nowHooks, event)
			} else {
				nowHooks[event] = filtered
			}
		}
		if len(nowHooks) == 0 && original["hooks"] == nil {
			delete(now, "hooks")
		}
	} else {
		servers, _ := now["mcpServers"].(map[string]any)
		wanted, _ := installed["mcpServers"].(map[string]any)
		if servers == nil || wanted == nil || !equal(servers["agent_intercom"], wanted["agent_intercom"]) {
			return nil, conflict
		}
		delete(servers, "agent_intercom")
		if len(servers) == 0 && original["mcpServers"] == nil {
			delete(now, "mcpServers")
		}
	}
	data, err := json.MarshalIndent(now, "", "  ")
	return append(data, '\n'), err
}
