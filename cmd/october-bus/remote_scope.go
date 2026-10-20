package main

import (
	"errors"
	"net/http"
	"net/url"
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
	token, err := readScopeTokenFile(tokenPath)
	if err != nil {
		return bus.Client{}, err
	}
	return bus.Client{Address: address, Token: token, HTTP: &http.Client{Timeout: 30 * time.Second, CheckRedirect: rejectBusRedirect}}, nil
}
