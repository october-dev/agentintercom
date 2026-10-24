package bus

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHostedIdentityRequiresAuthVerificationAndAccountAudience(t *testing.T) {
	var calls atomic.Int32
	var status atomic.Int32
	status.Store(200)
	const id = "fa4eb2b1-7e3d-4ff1-8288-a42d26466f16"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/auth/v1/user" || r.Header.Get("apikey") != "public-test-key" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, "unauthorized", 401)
			return
		}
		if status.Load() != 200 {
			w.WriteHeader(int(status.Load()))
			return
		}
		writeJSON(w, 200, map[string]any{"id": id, "email": "Owner@Example.test", "email_confirmed_at": "2026-01-01T00:00:00Z", "is_anonymous": false})
	}))
	defer server.Close()
	verifier, err := NewSupabaseIdentity(server.URL, "public-test-key", nil)
	requireNoError(t, err)
	token := func(change func(map[string]any)) string {
		claims := map[string]any{"sub": id, "iss": server.URL + "/auth/v1", "aud": "authenticated", "exp": time.Now().Add(time.Hour).Unix(), "role": "authenticated"}
		if change != nil {
			change(claims)
		}
		data, _ := json.Marshal(claims)
		return "e30." + base64.RawURLEncoding.EncodeToString(data) + ".test-signature"
	}
	valid := token(nil)
	user, err := verifier.Authenticate(context.Background(), valid)
	requireNoError(t, err)
	require(t, user.ID == id && user.Email == "owner@example.test", "wrong verified identity")
	_, err = verifier.Authenticate(context.Background(), valid)
	requireNoError(t, err)
	require(t, calls.Load() == 1, "positive identity cache did not reuse verification")
	for _, change := range []func(map[string]any){
		func(c map[string]any) { c["aud"] = "different-product" },
		func(c map[string]any) { c["iss"] = "https://attacker.example/auth/v1" },
		func(c map[string]any) { c["exp"] = 1 },
		func(c map[string]any) { c["role"] = "service_role" },
		func(c map[string]any) { c["client_id"] = "third-party-app" },
	} {
		_, err := verifier.Authenticate(context.Background(), token(change))
		requireCode(t, err, CodeUnauthenticated)
	}
	require(t, calls.Load() == 1, "rejected local claims reached the auth service")
	_, err = verifier.Authenticate(context.Background(), token(func(c map[string]any) { c["sub"] = "another-user" }))
	requireCode(t, err, CodeUnauthenticated)
	status.Store(401)
	_, err = verifier.Authenticate(context.Background(), token(func(c map[string]any) { c["nonce"] = "invalid-signature" }))
	requireCode(t, err, CodeUnauthenticated)
	status.Store(503)
	_, err = verifier.Authenticate(context.Background(), token(func(c map[string]any) { c["nonce"] = "outage" }))
	requireCode(t, err, CodeInternal)
	require(t, !strings.Contains(err.Error(), "public-test-key") && !strings.Contains(err.Error(), "test-signature"), "identity error exposed credentials")
}
