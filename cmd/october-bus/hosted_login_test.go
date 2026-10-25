package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHostedProfileAndConnectionKeepSecretsPrivate(t *testing.T) {
	t.Setenv("OCTOBER_BUS_DATA_DIR", t.TempDir())
	path, err := hostedProfilePath("team", true)
	requireNoError(t, err)
	profile := hostedProfile{Version: 1, Server: "https://bus.example.test", WorkspaceID: "workspace", DeviceID: "device", Token: "obd_" + strings.Repeat("A", 43), ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
	requireNoError(t, saveHostedProfile(path, profile))
	require(t, saveHostedProfile(path, profile) != nil, "credential file overwritten")
	read, err := readHostedProfile("team")
	requireNoError(t, err)
	require(t, read.Token == profile.Token, "credential changed")
	for _, format := range []string{"json", "toml", "entry"} {
		var output bytes.Buffer
		requireNoError(t, connectCommand([]string{"--hosted", "team", "--format", format}, &output))
		require(t, !strings.Contains(output.String(), profile.Token) && strings.Contains(output.String(), "--hosted") && strings.Contains(output.String(), "--new-session"), "unsafe or non-session configuration")
	}
	requireNoError(t, os.Chmod(path, 0644))
	_, err = readHostedProfile("team")
	require(t, err != nil, "shared credential permissions accepted")
	requireNoError(t, os.Chmod(path, 0600))
	requireNoError(t, os.Symlink(path, filepath.Join(filepath.Dir(path), "link.json")))
	_, err = readHostedProfile("link")
	require(t, err != nil, "symlink credential accepted")
	profile.ExpiresAt = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	data, _ := json.Marshal(profile)
	requireNoError(t, os.WriteFile(path, data, 0600))
	_, err = readHostedProfile("team")
	require(t, err != nil, "expired credential accepted")
	for _, name := range []string{"../escape", "a/b", "bad\nname", ""} {
		_, err := hostedProfilePath(name, true)
		require(t, err != nil, "invalid profile path accepted")
	}
	for _, server := range []string{"http://bus.example.test", "https://user:pass@example.test", "https://bus.example.test/path", "https://bus.example.test?", "https://bus.example.test#secret"} {
		_, err := hostedOrigin(server)
		require(t, err != nil, "unsafe hosted origin accepted")
	}
}
