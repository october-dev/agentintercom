package a2abridge_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient/agentcard"
	"github.com/october-dev/october-bus/a2abridge"
)

func TestAgentCardMapsPublicAgentFields(t *testing.T) {
	card, err := a2abridge.NewAgentCard(a2abridge.AgentProfile{
		DisplayName: "Reviewer",
		Capabilities: []a2abridge.Capability{
			{Name: "code_review", Description: "Reviews code changes."},
		},
	}, a2abridge.CardOptions{
		InterfaceURL: "https://agents.example.com/reviewer",
		Version:      "1.2.3",
	})
	requireNoError(t, err)
	require(t, card.Name == "Reviewer" && card.Version == "1.2.3" && len(card.Skills) == 1, "unexpected card: %#v", card)
	if card.Skills[0].ID != "code_review" || card.Skills[0].Description != "Reviews code changes." {
		t.Fatalf("unexpected skills: %#v", card.Skills)
	}
	if len(card.SupportedInterfaces) != 1 || card.SupportedInterfaces[0].ProtocolBinding != a2a.TransportProtocolHTTPJSON || card.SupportedInterfaces[0].ProtocolVersion != a2a.Version {
		t.Fatalf("unexpected interfaces: %#v", card.SupportedInterfaces)
	}
	if _, ok := card.SecuritySchemes["bearer"].(a2a.HTTPAuthSecurityScheme); !ok {
		t.Fatalf("bearer security scheme is missing: %#v", card.SecuritySchemes)
	}
	encoded, err := json.Marshal(card)
	requireNoError(t, err)
	for _, privateField := range []string{"executionId", "agentId", "scopeId", "scopeToken", "agentToken"} {
		require(t, !strings.Contains(string(encoded), privateField), "card contains private field %q", privateField)
	}
}

func TestAgentCardRequiresSecureRemoteURL(t *testing.T) {
	agent := a2abridge.AgentProfile{DisplayName: "Reviewer"}
	for _, value := range []string{
		"http://example.com/a2a",
		"https://user:secret@example.com/a2a",
		"https://example.com/a2a?token=secret",
		"file:///tmp/a2a",
	} {
		if _, err := a2abridge.NewAgentCard(agent, a2abridge.CardOptions{InterfaceURL: value}); err == nil {
			t.Fatalf("expected %q to be rejected", value)
		}
	}
	for _, value := range []string{
		"http://127.0.0.1:8080/a2a",
		"http://[::1]:8080/a2a",
		"http://localhost:8080/a2a",
		"https://agents.example.com/a2a",
	} {
		if _, err := a2abridge.NewAgentCard(agent, a2abridge.CardOptions{InterfaceURL: value}); err != nil {
			t.Fatalf("expected %q to be accepted: %v", value, err)
		}
	}
}

func TestAgentCardRejectsIncompleteProvider(t *testing.T) {
	for _, tc := range []struct{ name, organization, url, want string }{
		{"missing-url", "Example Labs", "", "must be set together"},
		{"missing-organization", "", "https://example.com", "must be set together"},
		{"blank-organization", "   ", "https://example.com", "must not be blank"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := a2abridge.NewAgentCard(a2abridge.AgentProfile{DisplayName: "Reviewer"}, a2abridge.CardOptions{
				InterfaceURL:         "https://agents.example.com/reviewer",
				ProviderOrganization: tc.organization, ProviderURL: tc.url,
			})
			require(t, err != nil && strings.Contains(err.Error(), tc.want), "expected %q, got %v", tc.want, err)
		})
	}
}

func TestAgentCardOptionalFieldsOmittedWhenUnset(t *testing.T) {
	agent := a2abridge.AgentProfile{DisplayName: "Reviewer"}
	card, err := a2abridge.NewAgentCard(agent, a2abridge.CardOptions{
		InterfaceURL: "https://agents.example.com/reviewer",
	})
	requireNoError(t, err)
	require(t, card.Provider == nil, "provider should be omitted: %#v", card.Provider)
	if card.DocumentationURL != "" {
		t.Errorf("DocumentationURL = %q, want empty", card.DocumentationURL)
	}
	if card.IconURL != "" {
		t.Errorf("IconURL = %q, want empty", card.IconURL)
	}
	// Confirm the omitted fields stay absent from the public JSON.
	encoded, err := json.Marshal(card)
	requireNoError(t, err)
	for _, unwanted := range []string{"\"documentationUrl\"", "\"iconUrl\"", "\"provider\""} {
		if strings.Contains(string(encoded), unwanted) {
			t.Errorf("encoded card contains %q but should omit it: %s", unwanted, encoded)
		}
	}
}

// TestAgentCardRejectsInvalidPublicURL exercises validatePublicURL through
// every field that uses it. Each case must return a clear, field-prefixed
// error and must not produce a card.
func TestAgentCardRejectsInvalidPublicURL(t *testing.T) {
	for _, tc := range []struct {
		name    string
		options a2abridge.CardOptions
		wantErr string
	}{
		{"provider-credentials", a2abridge.CardOptions{ProviderOrganization: "Example Labs", ProviderURL: "https://user:placeholder@example.com"}, "providerUrl"},
		{"documentation-file", a2abridge.CardOptions{DocumentationURL: "file:///etc/passwd"}, "documentationUrl"},
		{"icon-query", a2abridge.CardOptions{IconURL: "https://cdn.example.com/icon.png?token=placeholder"}, "iconUrl"},
		{"documentation-fragment", a2abridge.CardOptions{DocumentationURL: "https://docs.example.com/reviewer#placeholder"}, "documentationUrl"},
		{"provider-relative", a2abridge.CardOptions{ProviderOrganization: "Example Labs", ProviderURL: "/relative/path"}, "providerUrl"},
		{"icon-empty-host", a2abridge.CardOptions{IconURL: "https://"}, "iconUrl"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.options.InterfaceURL = "https://agents.example.com/reviewer"
			card, err := a2abridge.NewAgentCard(a2abridge.AgentProfile{DisplayName: "Reviewer"}, tc.options)
			require(t, err != nil && strings.Contains(err.Error(), tc.wantErr), "expected %s error, got card %#v, %v", tc.wantErr, card, err)
		})
	}
}

// TestAgentCardPublicURLsAllowPlainHTTP checks the carve-out from
// validateInterfaceURL: a documentation/icon/provider URL on plain HTTP is
// allowed because it's a link clients follow rather than an endpoint they
// POST to. The interface URL still requires HTTPS for non-loopback hosts.
func TestAgentCardPublicURLsAllowPlainHTTP(t *testing.T) {
	agent := a2abridge.AgentProfile{DisplayName: "Reviewer"}
	card, err := a2abridge.NewAgentCard(agent, a2abridge.CardOptions{
		InterfaceURL:         "http://127.0.0.1:8080/a2a",
		ProviderOrganization: "Example Labs",
		ProviderURL:          "http://example.com",
		DocumentationURL:     "http://docs.internal/reviewer",
		IconURL:              "http://cdn.internal/icon.png",
	})
	require(t, err == nil, "plain-http public URLs should be accepted, got %v", err)
	if card.DocumentationURL != "http://docs.internal/reviewer" {
		t.Errorf("DocumentationURL = %q", card.DocumentationURL)
	}
	if card.IconURL != "http://cdn.internal/icon.png" {
		t.Errorf("IconURL = %q", card.IconURL)
	}
	if card.Provider == nil || card.Provider.URL != "http://example.com" {
		t.Errorf("Provider.URL not propagated: %+v", card.Provider)
	}
}

func TestAgentCardFullConfigurationRoundTrip(t *testing.T) {
	agent := a2abridge.AgentProfile{DisplayName: "Reviewer"}
	card, err := a2abridge.NewAgentCard(agent, a2abridge.CardOptions{
		InterfaceURL:         "https://agents.example.com/reviewer",
		ProviderOrganization: "Example Labs",
		ProviderURL:          "https://example.com",
		DocumentationURL:     "https://docs.example.com/reviewer",
		IconURL:              "https://cdn.example.com/reviewer.png",
		Version:              "1.2.3",
		Description:          "Reviews code changes.",
	})
	requireNoError(t, err)
	require(t, card.Provider != nil, "provider is missing")
	require(t, card.Provider.Org == "Example Labs" && card.Provider.URL == "https://example.com", "unexpected provider: %#v", card.Provider)
	require(t, card.DocumentationURL == "https://docs.example.com/reviewer" && card.IconURL == "https://cdn.example.com/reviewer.png", "unexpected public URLs: %#v", card)
	encoded, err := json.Marshal(card)
	requireNoError(t, err)
	body := string(encoded)
	for _, want := range []string{
		`"organization":"Example Labs"`,
		`"url":"https://example.com"`,
		`"documentationUrl":"https://docs.example.com/reviewer"`,
		`"iconUrl":"https://cdn.example.com/reviewer.png"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("encoded card missing %q\n--- card ---\n%s", want, body)
		}
	}
	// Defence in depth — credentials and execution IDs must never appear in
	// the public card even when every optional field is configured.
	for _, secret := range []string{"execution-secret", "scopeToken", "agentToken"} {
		require(t, !strings.Contains(body, secret), "encoded card contains private value %q", secret)
	}
}

func TestAgentCardHandlerWorksWithOfficialResolver(t *testing.T) {
	card, err := a2abridge.NewAgentCard(a2abridge.AgentProfile{DisplayName: "Reviewer"}, a2abridge.CardOptions{
		InterfaceURL: "https://agents.example.com/reviewer",
		Version:      "1.2.3",
	})
	requireNoError(t, err)
	handler, err := a2abridge.NewAgentCardHandler(card)
	requireNoError(t, err)
	mux := http.NewServeMux()
	mux.Handle(a2abridge.AgentCardPath, handler)
	server := httptest.NewServer(mux)
	defer server.Close()

	resolver := agentcard.NewResolver(server.Client())
	resolved, err := resolver.Resolve(context.Background(), server.URL)
	requireNoError(t, err)
	require(t, resolved.Name == card.Name && resolved.Version == card.Version && len(resolved.SupportedInterfaces) == 1, "unexpected resolved card: %#v", resolved)

	response, err := server.Client().Get(server.URL + a2abridge.AgentCardPath)
	requireNoError(t, err)
	response.Body.Close()
	if response.Header.Get("Cache-Control") != "public, max-age=60" || response.Header.Get("ETag") == "" || response.Header.Get("Last-Modified") == "" {
		t.Fatalf("missing cache headers: %#v", response.Header)
	}

	request, err := http.NewRequest(http.MethodGet, server.URL+a2abridge.AgentCardPath, nil)
	requireNoError(t, err)
	request.Header.Set("If-None-Match", response.Header.Get("ETag"))
	cached, err := server.Client().Do(request)
	requireNoError(t, err)
	cached.Body.Close()
	if cached.StatusCode != http.StatusNotModified {
		t.Fatalf("unexpected cache response: %d", cached.StatusCode)
	}
}

func TestAgentCardHandlerRejectsUnsupportedMethods(t *testing.T) {
	card, err := a2abridge.NewAgentCard(a2abridge.AgentProfile{DisplayName: "Reviewer"}, a2abridge.CardOptions{
		InterfaceURL: "https://agents.example.com/reviewer",
	})
	requireNoError(t, err)
	handler, err := a2abridge.NewAgentCardHandler(card)
	requireNoError(t, err)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, a2abridge.AgentCardPath, nil))
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != "GET, HEAD, OPTIONS" {
		t.Fatalf("unexpected response: %d, %q", response.Code, response.Header().Get("Allow"))
	}
}

// makeTestCard returns a small valid card used by the cache-policy tests
// below. Each test calls it fresh so failures cannot bleed between cases.
func makeTestCard(t *testing.T) *a2a.AgentCard {
	t.Helper()
	card, err := a2abridge.NewAgentCard(a2abridge.AgentProfile{DisplayName: "Reviewer"}, a2abridge.CardOptions{
		InterfaceURL: "https://agents.example.com/reviewer",
		Version:      "1.2.3",
	})
	requireNoError(t, err)
	return card
}

func TestAgentCardHandlerCacheLifetime(t *testing.T) {
	for _, tc := range []struct {
		name            string
		lifetime        time.Duration
		header, wantErr string
	}{
		{"custom", 5 * time.Minute, "public, max-age=300", ""},
		{"zero", 0, "public, max-age=0", ""},
		{"negative", -time.Second, "", "must not be negative"},
		{"excessive", 48 * time.Hour, "", "exceeds the maximum"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler, err := a2abridge.NewAgentCardHandlerWithOptions(makeTestCard(t), a2abridge.HandlerOptions{CacheLifetime: tc.lifetime})
			if tc.wantErr != "" {
				require(t, err != nil && strings.Contains(err.Error(), tc.wantErr), "expected %s, got %v", tc.wantErr, err)
				return
			}
			requireNoError(t, err)
			response := cardResponse(handler, nil)
			require(t, response.Header().Get("Cache-Control") == tc.header, "unexpected cache header: %v", response.Header())
		})
	}
}

func cardResponse(handler http.Handler, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, a2abridge.AgentCardPath, nil)
	for name, value := range headers {
		if value != "" {
			request.Header.Set(name, value)
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestAgentCardHandlerConditionalRequests(t *testing.T) {
	fixed := time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC)
	handler, err := a2abridge.NewAgentCardHandlerWithOptions(makeTestCard(t), a2abridge.HandlerOptions{
		CacheLifetime: time.Minute, LastModified: fixed,
	})
	requireNoError(t, err)
	initial := cardResponse(handler, nil)
	require(t, initial.Header().Get("Last-Modified") == fixed.Format(http.TimeFormat), "unexpected Last-Modified: %v", initial.Header())
	etag := initial.Header().Get("ETag")
	for _, tc := range []struct {
		name, ifNoneMatch, ifModifiedSince string
		status                             int
	}{
		{"matching-date", "", fixed.Format(http.TimeFormat), http.StatusNotModified},
		{"stale-date", "", fixed.Add(-time.Hour).Format(http.TimeFormat), http.StatusOK},
		{"invalid-date", "", "not-a-valid-http-date", http.StatusOK},
		{"etag-precedes-date", `"different"`, fixed.Format(http.TimeFormat), http.StatusOK},
		{"weak-etag", "W/" + etag, "", http.StatusNotModified},
		{"etag-list", `"different", ` + etag, "", http.StatusNotModified},
		{"wildcard-etag", "*", "", http.StatusNotModified},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := cardResponse(handler, map[string]string{"If-None-Match": tc.ifNoneMatch, "If-Modified-Since": tc.ifModifiedSince})
			require(t, response.Code == tc.status, "unexpected status: %d", response.Code)
			if tc.status == http.StatusOK {
				require(t, response.Body.Len() > 0, "full response has no body")
			}
		})
	}
}

func TestAgentCardHandlerCacheHeadersStayConsistent(t *testing.T) {
	handler, err := a2abridge.NewAgentCardHandlerWithOptions(makeTestCard(t), a2abridge.HandlerOptions{
		CacheLifetime: 30 * time.Second, LastModified: time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC),
	})
	requireNoError(t, err)
	initial := cardResponse(handler, nil).Header()
	for i := 0; i < 4; i++ {
		headers := cardResponse(handler, nil).Header()
		for _, name := range []string{"ETag", "Cache-Control", "Last-Modified"} {
			require(t, headers.Get(name) == initial.Get(name), "%s drifted: %v", name, headers)
		}
	}
}
