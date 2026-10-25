package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/october-dev/october-bus/bus"
)

func TestGatewayProvisioningKeepsKeysPrivateAndNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OCTOBER_BUS_DATA_DIR", filepath.Join(dir, "data"))
	paths, err := bus.DefaultDaemonPaths()
	requireNoError(t, err)
	requireNoError(t, bus.SaveScopeToken(paths.DataDir, "pilot", base64.RawURLEncoding.EncodeToString(make([]byte, 32))))
	configPath, keyPath := filepath.Join(dir, "gateway.json"), filepath.Join(dir, "muse.key")
	args := []string{"--public-url", "https://bus.example.test", "--scope", "pilot", "--config", configPath, "--key-file", keyPath}
	var output bytes.Buffer
	requireNoError(t, captureStdout(&output, func() error { return initGateway(args) }))
	key, err := os.ReadFile(keyPath)
	requireNoError(t, err)
	key = bytes.TrimSpace(key)
	require(t, len(key) >= 43 && !bytes.Contains(output.Bytes(), key), "secret appeared in provisioning output")
	config, err := readGatewayConfig(configPath)
	requireNoError(t, err)
	digest := sha256.Sum256(key)
	require(t, config.Clients[0].APIKeySHA256 == hex.EncodeToString(digest[:]), "wrong key hash")
	configBytes, err := os.ReadFile(configPath)
	requireNoError(t, err)
	require(t, !bytes.Contains(configBytes, key), "configuration stored the plaintext key")
	for _, path := range []string{keyPath, configPath} {
		info, err := os.Stat(path)
		requireNoError(t, err)
		if runtime.GOOS != "windows" {
			require(t, info.Mode().Perm() == 0o600, "file is not private: %s", path)
		}
	}
	require(t, initGateway(args) != nil, "provisioning overwrote existing credentials")
	still, err := os.ReadFile(keyPath)
	requireNoError(t, err)
	require(t, bytes.Equal(bytes.TrimSpace(still), key), "existing key changed")
	_, err = writeGatewayKey(keyPath)
	require(t, err != nil, "key generator overwrote a credential")
}

func TestGatewayRejectsSharedConfigAndPublicListener(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gateway.json")
	valid := `{"publicURL":"https://bus.example.test","clients":[{"scope":"pilot","agent":"muse","name":"Muse","apiKeySha256":"` + strings.Repeat("a", 64) + `"}]}`
	requireNoError(t, os.WriteFile(path, []byte(valid), 0o600))
	_, err := readGatewayConfig(path)
	requireNoError(t, err)
	if runtime.GOOS != "windows" {
		requireNoError(t, os.Chmod(path, 0o644))
		_, err = readGatewayConfig(path)
		require(t, err != nil, "shared config accepted")
		requireNoError(t, os.Chmod(path, 0o600))
	}
	for _, input := range []string{valid + `{}`, strings.Replace(valid, `"clients":`, `"unexpected":true,"clients":`, 1)} {
		requireNoError(t, os.WriteFile(path, []byte(input), 0o600))
		_, err = readGatewayConfig(path)
		require(t, err != nil, "ambiguous config accepted")
	}
	for _, listen := range []string{"0.0.0.0:8787", ":8787", "[::]:8787", "example.com:8787"} {
		err := startGateway([]string{"--config", path, "--listen", listen})
		require(t, err != nil && strings.Contains(err.Error(), "loopback"), "public listener not refused: %s %v", listen, err)
	}
}

func TestAwaitDaemonWaitsThenFailsWithoutDaemon(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "bus.json")
	started := time.Now()
	_, err := awaitDaemon(context.Background(), missing, 1500*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "start the local Bus daemon first") {
		t.Fatalf("expected daemon-not-ready error, got %v", err)
	}
	if elapsed := time.Since(started); elapsed < time.Second {
		t.Fatalf("gateway gave up after %s without waiting for the daemon", elapsed)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := awaitDaemon(ctx, missing, time.Minute); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wait should return context error, got %v", err)
	}
}
