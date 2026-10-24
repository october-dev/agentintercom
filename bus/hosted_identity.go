package bus

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

type HostedIdentityVerifier interface {
	Authenticate(context.Context, string) (HostedUser, error)
}

type cachedHostedIdentity struct {
	user  HostedUser
	until time.Time
}

// SupabaseIdentity verifies account sessions with the configured Auth service.
// This supports signing-key rotation and legacy projects without accepting an
// unverified JWT. Only short positive results are cached, by token digest.
type SupabaseIdentity struct {
	origin  string
	key     string
	issuers map[string]bool
	client  *http.Client
	mu      sync.Mutex
	cache   map[string]cachedHostedIdentity
	pending singleflight.Group
}

func NewSupabaseIdentity(origin, publicKey string, additionalIssuers []string) (*SupabaseIdentity, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || !isPublicAuthKey(publicKey) {
		return nil, errors.New("configure an authentication origin and public API key")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && ip != nil && ip.IsLoopback()) {
		return nil, errors.New("remote authentication requires HTTPS")
	}
	origin = strings.TrimRight(origin, "/")
	issuers := map[string]bool{origin + "/auth/v1": true}
	for _, issuer := range additionalIssuers {
		i, err := url.Parse(issuer)
		if err != nil || i.Scheme != "https" || i.Host == "" || i.Path != "/auth/v1" || i.User != nil || i.RawQuery != "" || i.Fragment != "" {
			return nil, errors.New("additional authentication issuers must be exact HTTPS Auth URLs")
		}
		issuers[issuer] = true
	}
	return &SupabaseIdentity{origin: origin, key: publicKey, issuers: issuers, cache: map[string]cachedHostedIdentity{}, client: &http.Client{
		Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("authentication redirect refused") },
	}}, nil
}

// Only browser-safe key classes may be used for account sign-in. Never expose
// service_role or secret keys through the workspace's public configuration.
func isPublicAuthKey(key string) bool {
	if strings.HasPrefix(key, "sb_publishable_") && len(key) >= 30 && len(key) <= 256 {
		return !strings.ContainsAny(key, " \r\n\t")
	}
	parts := strings.Split(key, ".")
	if len(parts) != 3 || len(key) > 4096 {
		return false
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	var claims struct {
		Role string `json:"role"`
	}
	return err == nil && json.Unmarshal(data, &claims) == nil && claims.Role == "anon"
}

func (v *SupabaseIdentity) Authenticate(ctx context.Context, token string) (HostedUser, error) {
	refused := Errorf(CodeUnauthenticated, "Sign in to October with a verified email address")
	if len(token) < 20 || len(token) > 8192 {
		return HostedUser{}, refused
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return HostedUser{}, refused
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return HostedUser{}, refused
	}
	var claims struct {
		Subject  string          `json:"sub"`
		Issuer   string          `json:"iss"`
		Audience json.RawMessage `json:"aud"`
		Expiry   int64           `json:"exp"`
		Role     string          `json:"role"`
		ClientID string          `json:"client_id"`
	}
	if json.Unmarshal(data, &claims) != nil || claims.Expiry <= time.Now().Unix() || !v.issuers[claims.Issuer] || claims.Role != "authenticated" || claims.ClientID != "" || !identityPattern.MatchString(claims.Subject) {
		return HostedUser{}, refused
	}
	var audience string
	var audiences []string
	if json.Unmarshal(claims.Audience, &audience) == nil {
		audiences = []string{audience}
	} else if json.Unmarshal(claims.Audience, &audiences) != nil {
		return HostedUser{}, refused
	}
	validAudience := false
	for _, a := range audiences {
		validAudience = validAudience || a == "authenticated"
	}
	if !validAudience {
		return HostedUser{}, refused
	}
	digest := tokenDigest(token)
	v.mu.Lock()
	cached, ok := v.cache[digest]
	v.mu.Unlock()
	if ok && cached.until.After(time.Now()) {
		return cached.user, nil
	}
	result, err, _ := v.pending.Do(digest, func() (any, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, v.origin+"/auth/v1/user", nil)
		if err != nil {
			return HostedUser{}, err
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("apikey", v.key)
		response, err := v.client.Do(request)
		if err != nil {
			return HostedUser{}, Errorf(CodeInternal, "October sign-in could not be checked; try again shortly")
		}
		defer response.Body.Close()
		if response.StatusCode == 401 || response.StatusCode == 403 {
			return HostedUser{}, refused
		}
		if response.StatusCode != 200 {
			return HostedUser{}, Errorf(CodeInternal, "October sign-in could not be checked; try again shortly")
		}
		var body struct {
			ID               string `json:"id"`
			Email            string `json:"email"`
			EmailConfirmedAt string `json:"email_confirmed_at"`
			Anonymous        bool   `json:"is_anonymous"`
		}
		if json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&body) != nil || body.ID != claims.Subject || body.EmailConfirmedAt == "" || body.Anonymous {
			return HostedUser{}, refused
		}
		email, err := normalizedEmail(body.Email)
		if err != nil {
			return HostedUser{}, refused
		}
		user := HostedUser{ID: body.ID, Email: email}
		until := time.Now().Add(30 * time.Second)
		if expiry := time.Unix(claims.Expiry, 0); expiry.Before(until) {
			until = expiry
		}
		v.mu.Lock()
		if len(v.cache) >= 4096 {
			for key := range v.cache {
				delete(v.cache, key)
				break
			}
		}
		v.cache[digest] = cachedHostedIdentity{user: user, until: until}
		v.mu.Unlock()
		return user, nil
	})
	if err != nil {
		return HostedUser{}, err
	}
	return result.(HostedUser), nil
}
