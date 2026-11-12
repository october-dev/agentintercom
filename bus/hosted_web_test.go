package bus

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHostedBrowserAssetsAndConfig(t *testing.T) {
	f := newHostedOAuthFixture(t)
	identity, err := NewSupabaseIdentity("https://auth.example.test", "sb_publishable_fixture_key_for_tests", nil)
	requireNoError(t, err)
	f.h.identity = identity
	for _, path := range []string{"/app", "/assets/app.js", "/assets/style.css"} {
		w := f.call("GET", path, "", "")
		require(t, w.Code == 200, "asset unavailable %s: %d", path, w.Code)
		require(t, strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") && !strings.Contains(w.Header().Get("Content-Security-Policy"), "unsafe-inline"), "missing strict browser policy")
	}
	config := f.call("GET", "/api/config", "", "")
	require(t, config.Code == 200 && strings.Contains(config.Body.String(), "sb_publishable_fixture_key_for_tests") && !strings.Contains(config.Body.String(), "encryption"), "unsafe or missing config")
	bad := httptest.NewRequest("GET", "https://bus.example.test/api/config", nil)
	bad.Header.Set("Origin", "https://untrusted.example")
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, bad)
	require(t, w.Code == 403, "foreign origin read browser config")
	require(t, f.call("GET", "/assets/../hosted_accounts.go", "", "").Code != 200, "served a source file")
}
