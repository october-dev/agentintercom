// Package gateway exposes an operator-provisioned MCP connector while keeping
// the daemon and its administrative routes on loopback.
package gateway

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/october-dev/october-bus/bus"
)

// ClientConfig maps one connector API key to one dedicated agent identity.
// Peer links are created by the remote bridges (`--connect-to <agent>`), not
// here: registration links inside the same transaction and fails when the peer
// is not registered yet, which is always the case on a fresh deployment.
type ClientConfig struct {
	Scope        string `json:"scope"`
	Agent        string `json:"agent"`
	Name         string `json:"name"`
	APIKeySHA256 string `json:"apiKeySha256"`
}

type Config struct {
	PublicURL string         `json:"publicURL"`
	Clients   []ClientConfig `json:"clients"`
}

// Validate rejects ambiguous credentials and identities before any registration.
func (c Config) Validate() error {
	u, err := url.Parse(c.PublicURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return errors.New("publicURL must be an HTTPS origin without a path, credentials, query, or fragment")
	}
	if len(c.Clients) == 0 || len(c.Clients) > 128 {
		return errors.New("configure between 1 and 128 connector clients")
	}
	keys, identities := map[string]bool{}, map[string]bool{}
	for i, client := range c.Clients {
		for _, id := range []string{client.Scope, client.Agent} {
			if _, err := bus.ScopeTokenPath("", id); err != nil {
				return fmt.Errorf("client %d has an invalid scope or agent ID", i)
			}
		}
		if strings.TrimSpace(client.Name) == "" || len(client.Name) > 256 {
			return fmt.Errorf("client %d has an invalid name", i)
		}
		digest, err := hex.DecodeString(client.APIKeySHA256)
		if err != nil || len(digest) != sha256.Size {
			return fmt.Errorf("client %d requires a SHA-256 API key digest", i)
		}
		key, identity := string(digest), client.Scope+"/"+client.Agent
		if keys[key] || identities[identity] {
			return errors.New("connector API keys and scope/agent pairs must be unique")
		}
		keys[key], identities[identity] = true, true
	}
	return nil
}

type client struct {
	digest  []byte
	session *bus.AgentSession
	budget  chan struct{}
}

// Gateway admission mirrors the daemon's own request/control split. An
// unauthenticated request never occupies control or budget, and occupies
// admission only for the duration of one header-only loopback call.
//
//	admission  credential probes, /health/ready, /bus/health*   (32)
//	control    validated heartbeat and retirement                 (32)
//	budget     validated everything else, and connector /mcp     (128)
//
// Sizes are bounds, not isolation proofs: heartbeats compete for admission
// with all unauthenticated traffic. The probe uses the daemon's dedicated
// classification pool, so neither long-lived daemon requests nor a burst of
// invalid probes can take slots from local or remote heartbeats.
type Gateway struct {
	origin   string
	host     string
	upstream string
	clients  []client
	proxy    *httputil.ReverseProxy
	probe    *http.Client
	// probeTimeout bounds one credential probe; tests raise it to park probes.
	probeTimeout time.Duration
	agentMux     *http.ServeMux
	admission    chan struct{}
	control      chan struct{}
	budget       chan struct{}
	done         chan struct{}
	failOnce     sync.Once
	failed       atomic.Int32
}

// New starts one execution per connector, not per HTTP request. The execution
// describes the gateway, never evidence that a remote model is awake or idle.
// The resolver only runs at startup; scope credentials are never sent to clients.
func New(ctx context.Context, upstream string, config Config, resolve func(string) (string, error)) (_ *Gateway, err error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	u, err := url.Parse(upstream)
	if err != nil {
		return nil, errors.New("invalid upstream address")
	}
	ip, err := netip.ParseAddr(u.Hostname())
	if err != nil || !ip.IsLoopback() || u.Scheme != "http" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("upstream must be a loopback HTTP origin")
	}
	public, _ := url.Parse(config.PublicURL)
	// One upstream transport with enough idle connections for every pool, so
	// the probe and the proxy reuse loopback connections instead of opening
	// one per request (DefaultTransport keeps only two idle per host).
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns, transport.MaxIdleConnsPerHost = 256, 256
	g := &Gateway{
		origin: config.PublicURL, host: public.Host, upstream: upstream, done: make(chan struct{}),
		admission: make(chan struct{}, 32), control: make(chan struct{}, 32), budget: make(chan struct{}, 128),
		probe: &http.Client{Transport: transport, CheckRedirect: noRedirect}, probeTimeout: 5 * time.Second,
	}
	g.proxy = &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(u)
			r.Out.Host = u.Host
			// Never trust or forward client-supplied proxy identity or cookies.
			for _, name := range []string{"Forwarded", "Cookie", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-Ip"} {
				r.Out.Header.Del(name)
			}
		},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			failure(w, http.StatusBadGateway, "Bus is unavailable")
		},
	}
	defer func() {
		if err != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			_ = g.Close(cleanup)
		}
	}()
	// Resolve all credentials before registrations so an invalid later entry
	// cannot unnecessarily replace an earlier connector's active execution.
	tokens := make([]string, len(config.Clients))
	for i, entry := range config.Clients {
		tokens[i], err = resolve(entry.Scope)
		if err != nil {
			return nil, fmt.Errorf("resolve scope for client %d: %w", i, err)
		}
	}
	// A daemon without GET /v1/credential would answer every remote request
	// with 502. Refuse to start before any connector execution is displaced.
	probeCtx, cancelProbe := context.WithTimeout(ctx, g.probeTimeout)
	defer cancelProbe()
	if _, err := (bus.Client{Address: upstream, Token: tokens[0], HTTP: g.probe}).Credential(probeCtx, ""); err != nil {
		if bus.AsBusError(err).Code == bus.CodeNotFound {
			return nil, errors.New("daemon does not serve GET /v1/credential; upgrade the daemon before starting the gateway")
		}
		return nil, fmt.Errorf("credential probe for client 0: %w", err)
	}
	for i, entry := range config.Clients {
		// The connector accepts durable work on behalf of a pull-based client,
		// so it is idle and ready to receive; it never claims a model is awake.
		session, startErr := bus.StartAgentSession(ctx, bus.AgentSessionOptions{
			Address: upstream, ScopeToken: tokens[i], HeartbeatInterval: 5 * time.Second,
			HTTP:             &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: noRedirect},
			Registration:     bus.RegisterAgentInput{ID: entry.Agent, DisplayName: entry.Name, LeaseMS: 30_000},
			InitialLifecycle: bus.LifecycleIdle, InitialReady: true,
		})
		if startErr != nil {
			return nil, fmt.Errorf("start connector %d: %w", i, startErr)
		}
		digest, _ := hex.DecodeString(entry.APIKeySHA256)
		g.clients = append(g.clients, client{digest: digest, session: session, budget: make(chan struct{}, 16)})
		go func() {
			<-session.Done()
			// One scope owner can replace its connector execution. That must
			// not take unrelated scopes offline. Exit only when all have ended.
			if g.failed.Add(1) == int32(len(config.Clients)) {
				g.failOnce.Do(func() { close(g.done) })
			}
		}()
	}
	g.agentMux = g.remoteAgentRoutes()
	return g, nil
}

func noRedirect(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }

func (g *Gateway) Done() <-chan struct{} { return g.done }

func (g *Gateway) Close(ctx context.Context) error {
	g.failOnce.Do(func() { close(g.done) })
	var result error
	for _, c := range g.clients {
		result = errors.Join(result, c.session.Close(ctx))
	}
	return result
}

// failure closes the connection so net/http flushes the rejection before
// reading any of the request body (chunkWriter.writeHeader skips its pre-flush
// drain when closeAfterReply is set). Pool slots are released and the client
// sees the rejection at once; the connection goroutine may still discard a
// withheld body afterwards, bounded by the server's ReadTimeout.
func failure(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Connection", "close")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func unauthenticated(w http.ResponseWriter, message string) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="october-bus"`)
	failure(w, http.StatusUnauthorized, message)
}

func limited(w http.ResponseWriter, message string) {
	w.Header().Set("Retry-After", "1")
	failure(w, http.StatusTooManyRequests, message)
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Host != g.host || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != g.origin) {
		failure(w, http.StatusForbidden, "Host or Origin is not allowed")
		return
	}
	// Reject path aliases before routing. The private daemon must never see an
	// encoded slash, cleaned traversal, or redirect to an administrative route.
	if path.Clean(r.URL.Path) != r.URL.Path || strings.Contains(r.URL.EscapedPath(), "%") || strings.Contains(r.URL.Path, "\\") {
		failure(w, http.StatusNotFound, "Route not found")
		return
	}
	// Liveness answers before any budget so a flood cannot make the process
	// look dead to its supervisor.
	if r.URL.Path == "/health/live" && r.Method == http.MethodGet {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	select {
	case <-g.done:
		failure(w, http.StatusServiceUnavailable, "Connector is unavailable")
		return
	default:
	}
	if r.URL.Path == "/mcp" {
		// Authenticate before consuming the shared budget: unauthenticated
		// traffic must not be able to starve valid connectors.
		g.serveConnector(w, r)
		return
	}
	if r.URL.Path == "/health/ready" && r.Method == http.MethodGet {
		g.serveReady(w, r)
		return
	}
	g.agentMux.ServeHTTP(w, r)
}

// serveReady is unauthenticated, so it takes only the admission pool and never
// reads a client body.
func (g *Gateway) serveReady(w http.ResponseWriter, r *http.Request) {
	if !rejectBody(w, r) {
		return
	}
	release, ok := acquire(g.admission)
	if !ok {
		limited(w, "Concurrent request limit reached")
		return
	}
	defer release()
	for _, c := range g.clients {
		select {
		case <-c.session.Done():
			failure(w, http.StatusServiceUnavailable, "A connector is unavailable")
			return
		default:
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if _, err := (bus.Client{Address: g.upstream, HTTP: g.probe}).Health(ctx); err != nil {
		failure(w, http.StatusServiceUnavailable, "Bus is unavailable")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// rejectBody refuses a declared or chunked body on a route that is served
// without a credential. A held body would otherwise be drained inside the
// handler, while the admission slot is still occupied.
func rejectBody(w http.ResponseWriter, r *http.Request) bool {
	if r.ContentLength != 0 {
		failure(w, http.StatusBadRequest, "Health probes do not accept a request body")
		return false
	}
	return true
}

// acquire takes one slot from a bounded budget without blocking.
func acquire(budget chan struct{}) (release func(), ok bool) {
	select {
	case budget <- struct{}{}:
		return func() { <-budget }, true
	default:
		return nil, false
	}
}

func (g *Gateway) serveConnector(w http.ResponseWriter, r *http.Request) {
	scheme, key, found := strings.Cut(r.Header.Get("Authorization"), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || len(key) < 32 || len(key) > 256 || len(r.Header.Values("Authorization")) != 1 {
		unauthenticated(w, "Invalid connector API key")
		return
	}
	digest := sha256.Sum256([]byte(key))
	for _, c := range g.clients {
		if subtle.ConstantTimeCompare(digest[:], c.digest) != 1 {
			continue
		}
		select {
		case <-c.session.Done():
			failure(w, http.StatusServiceUnavailable, "Connector is unavailable")
			return
		default:
		}
		// The daemon serves MCP stateless with JSON responses; GET (SSE
		// listen) and DELETE (session end) have no meaning here.
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			failure(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		releaseClient, ok := acquire(c.budget)
		if !ok {
			limited(w, "Connector request limit reached")
			return
		}
		defer releaseClient()
		releaseGlobal, ok := acquire(g.budget)
		if !ok {
			limited(w, "Concurrent request limit reached")
			return
		}
		defer releaseGlobal()
		clone := r.Clone(r.Context())
		clone.Header.Set("Authorization", "Bearer "+c.session.Registration.AgentToken)
		g.proxy.ServeHTTP(w, clone)
		return
	}
	unauthenticated(w, "Invalid connector API key")
}

// Remote launchers authenticate directly to the existing Bus agent/scope APIs.
// This is an explicit allowlist: new daemon/admin APIs do not become public.
func (g *Gateway) remoteAgentRoutes() *http.ServeMux {
	mux := http.NewServeMux()
	patterns := []string{
		"GET /health", "GET /health/live", "GET /health/ready",
		"POST /mcp",
		"GET /v1/agents", "POST /v1/agents", "POST /v1/links",
		"GET /v1/me", "PATCH /v1/me/heartbeat", "POST /v1/me/retire", "GET /v1/peers",
		"GET /v1/me/wake", "POST /v1/me/wake/claim", "POST /v1/me/wake/attempts/{attemptId}", "POST /v1/me/wake/retry",
		"GET /v1/channels", "POST /v1/channels", "POST /v1/channels/{channelId}/subscription",
		"GET /v1/channel-posts", "POST /v1/channel-posts",
		"POST /v1/messages", "GET /v1/messages/{messageId}", "POST /v1/messages/ack",
		"POST /v1/inbox/reserve", "POST /v1/inbox/{reservationId}/commit", "POST /v1/inbox/{reservationId}/release",
		"GET /v1/tasks", "POST /v1/tasks", "GET /v1/tasks/page",
		"POST /v1/tasks/{taskId}/claim", "POST /v1/tasks/{taskId}/release", "POST /v1/tasks/{taskId}/complete",
		"GET /v1/tasks/{taskId}/progress", "POST /v1/tasks/{taskId}/progress",
		"POST /v1/escalations", "GET /v1/escalations/{escalationId}",
		"GET /v1/scope/escalations", "POST /v1/scope/escalations/{escalationId}/resolve", "GET /v1/events",
		"POST /outputs/{streamId}/values",
	}
	for _, pattern := range patterns {
		method, route, _ := strings.Cut(pattern, " ")
		var handler http.HandlerFunc
		// Same classes as the daemon's own ServeHTTP switch.
		switch route {
		case "/health", "/health/live", "/health/ready":
			handler = g.forwardHealth
		case "/v1/me/heartbeat", "/v1/me/retire":
			handler = g.forwardValidated(g.control, route == "/v1/me/retire")
		default:
			handler = g.forwardValidated(g.budget, false)
		}
		mux.HandleFunc(method+" /bus"+route, handler)
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { failure(w, http.StatusNotFound, "Route not found") })
	return mux
}

// forwardHealth proxies an unauthenticated, bodyless health probe under the
// admission pool and a bounded deadline.
func (g *Gateway) forwardHealth(w http.ResponseWriter, r *http.Request) {
	if !rejectBody(w, r) {
		return
	}
	release, ok := acquire(g.admission)
	if !ok {
		limited(w, "Concurrent request limit reached")
		return
	}
	defer release()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	clone := r.Clone(ctx)
	clone.URL.Path = strings.TrimPrefix(clone.URL.Path, "/bus")
	g.proxy.ServeHTTP(w, clone)
}

// forwardValidated proxies one allowlisted route only after the daemon has
// classified the bearer. The client body starts streaming only under a
// validated credential and a slot from the route's pool.
func (g *Gateway) forwardValidated(pool chan struct{}, retire bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		scheme, token, found := strings.Cut(r.Header.Get("Authorization"), " ")
		token = strings.TrimSpace(token)
		if !found || !strings.EqualFold(scheme, "Bearer") || token == "" || len(r.Header.Values("Authorization")) != 1 {
			unauthenticated(w, "Bearer token is required")
			return
		}
		if !g.admit(w, r, token, retire) {
			return
		}
		release, ok := acquire(pool)
		if !ok {
			limited(w, "Concurrent request limit reached")
			return
		}
		defer release()
		clone := r.Clone(r.Context())
		clone.URL.Path = strings.TrimPrefix(clone.URL.Path, "/bus")
		g.proxy.ServeHTTP(w, clone)
	}
}

// admit classifies the bearer with one header-only loopback call that the
// client cannot prolong, holding the admission pool only for its duration.
// The purpose comes from the matched route, never from the client's query.
// Success is classification only; the daemon still authenticates the request.
func (g *Gateway) admit(w http.ResponseWriter, r *http.Request, token string, retire bool) bool {
	release, ok := acquire(g.admission)
	if !ok {
		limited(w, "Concurrent request limit reached")
		return false
	}
	defer release()
	ctx, cancel := context.WithTimeout(r.Context(), g.probeTimeout)
	defer cancel()
	purpose := ""
	if retire {
		purpose = "retire"
	}
	_, err := (bus.Client{Address: g.upstream, Token: token, HTTP: g.probe}).Credential(ctx, purpose)
	switch {
	case err == nil:
		return true
	case bus.AsBusError(err).Code == bus.CodeUnauthenticated:
		unauthenticated(w, "Invalid credential")
	case bus.AsBusError(err).Code == bus.CodeBackpressure:
		limited(w, "Bus request limit reached")
	default:
		failure(w, http.StatusBadGateway, "Bus is unavailable")
	}
	return false
}
