package main

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/october-dev/october-bus/bus"
)

func rejectBusRedirect(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }

// A remote bridge loads scope authority outside the model loop, registers one
// execution, and forwards only the execution's MCP tools to the local harness.
func remoteScopeClient(address, tokenPath string) (bus.Client, error) {
	address = strings.TrimRight(address, "/")
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return bus.Client{}, errors.New("--remote must be an HTTPS Bus base URL without credentials, query, or fragment")
	}
	info, err := os.Lstat(tokenPath)
	if err != nil {
		return bus.Client{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > 128 || (runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0) {
		return bus.Client{}, errors.New("scope token must be an owner-only regular file of at most 128 bytes")
	}
	data, err := os.ReadFile(tokenPath)
	if err != nil {
		return bus.Client{}, err
	}
	token := strings.TrimSpace(string(data))
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(decoded) != 32 {
		return bus.Client{}, errors.New("scope token file is invalid")
	}
	return bus.Client{Address: address, Token: token, HTTP: &http.Client{Timeout: 30 * time.Second, CheckRedirect: rejectBusRedirect}}, nil
}
