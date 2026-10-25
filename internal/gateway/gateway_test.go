package gateway

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/october-dev/october-bus/bus"
	_ "modernc.org/sqlite"
)

type testTransport struct{ key string }

func (tr testTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	clone.Host = "bus.example.test"
	if tr.key != "" {
		clone.Header.Set("Authorization", "Bearer "+tr.key)
	}
	return http.DefaultTransport.RoundTrip(clone)
}

type fixture struct {
	runtime *bus.Runtime
	private *httptest.Server
	public  *httptest.Server
	g       *Gateway
	config  Config
	scopes  map[string]string
	keys    []string
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func setup(t *testing.T, count int) *fixture {
	return setupWith(t, count, ":memory:", nil)
}

// setupWith opens the daemon at source and, when wrap is set, points the
// gateway at the loopback origin wrap returns instead of the daemon itself.
func setupWith(t *testing.T, count int, source string, wrap func(daemon string) string) *fixture {
	t.Helper()
	runtime, err := bus.Open(source)
	must(t, err)
	t.Cleanup(func() { runtime.Close() })
	f := &fixture{runtime: runtime, scopes: map[string]string{}, config: Config{PublicURL: "https://bus.example.test"}}
	f.private = httptest.NewServer(bus.NewServer(runtime, bus.ServerOptions{AdminToken: "private-admin-token"}))
	t.Cleanup(f.private.Close)
	upstream := f.private.URL
	if wrap != nil {
		upstream = wrap(upstream)
	}
	for i := 0; i < count; i++ {
		scope, err := runtime.CreateScope(context.Background(), bus.CreateScopeInput{ID: fmt.Sprintf("owner-%d", i)})
		must(t, err)
		f.scopes[scope.ScopeID] = scope.ScopeToken
		key := strings.Repeat(fmt.Sprint(i), 48)
		f.keys = append(f.keys, key)
		digest := sha256.Sum256([]byte(key))
		f.config.Clients = append(f.config.Clients, ClientConfig{Scope: scope.ScopeID, Agent: "muse", Name: "Muse connector", APIKeySHA256: hex.EncodeToString(digest[:])})
	}
	f.g, err = New(context.Background(), upstream, f.config, func(scope string) (string, error) { return f.scopes[scope], nil })
	must(t, err)
	t.Cleanup(func() { f.g.Close(context.Background()) })
	f.public = httptest.NewServer(f.g)
	t.Cleanup(f.public.Close)
	return f
}

// busClient talks to the daemon through the public gateway's /bus surface.
func (f *fixture) busClient(token string) bus.Client {
	return bus.Client{Address: f.public.URL + "/bus", Token: token, HTTP: &http.Client{Transport: testTransport{}, Timeout: 5 * time.Second}}
}

// registerWorker registers an execution through the gateway without a
// heartbeat loop, so a test can fill pools without racing a background beat.
func (f *fixture) registerWorker(t *testing.T, scope, id string) bus.Client {
	t.Helper()
	registration, err := f.busClient(f.scopes[scope]).RegisterAgent(context.Background(), bus.RegisterAgentInput{ID: id, DisplayName: "Laptop agent", ConnectTo: []string{"muse"}})
	must(t, err)
	t.Cleanup(func() {
		_ = bus.Client{Address: f.private.URL, Token: registration.AgentToken}.Retire(context.Background())
	})
	return f.busClient(registration.AgentToken)
}

// pools reports the occupancy of every gateway pool.
func (f *fixture) pools() string {
	return fmt.Sprintf("admission=%d control=%d budget=%d", len(f.g.admission), len(f.g.control), len(f.g.budget))
}

// requirePools waits briefly for the pools to reach the given occupancy. A
// rejection releases its slot before the response is flushed, but a proxied
// response reaches the client before the handler's deferred release runs.
func requirePools(t *testing.T, f *fixture, admission, control, budget int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if len(f.g.admission) == admission && len(f.g.control) == control && len(f.g.budget) == budget {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pools did not settle at admission=%d control=%d budget=%d: %s", admission, control, budget, f.pools())
		}
		time.Sleep(time.Millisecond)
	}
}

func requirePoolsEmpty(t *testing.T, f *fixture) {
	t.Helper()
	requirePools(t, f, 0, 0, 0)
}

// request sends one request through the public listener and returns the
// status and body, so a test can tell a gateway rejection (bare JSON) from a
// daemon rejection (Bus envelope).
func (f *fixture) request(t *testing.T, method, path, token string, body any) (int, string) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		must(t, err)
		reader = bytes.NewReader(data)
	}
	r, err := http.NewRequest(method, f.public.URL+path, reader)
	must(t, err)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	response, err := (&http.Client{Transport: testTransport{key: token}, Timeout: 5 * time.Second}).Do(r)
	must(t, err)
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	must(t, err)
	return response.StatusCode, string(data)
}

// requireProbeRejected requires the credential probe, not the daemon, to
// have rejected the request: the gateway writes a bare {"error": string}
// while the daemon writes the Bus envelope with an ok field.
func requireProbeRejected(t *testing.T, f *fixture, method, path, token string, body any) {
	t.Helper()
	status, text := f.request(t, method, path, token, body)
	var rejection map[string]any
	if err := json.Unmarshal([]byte(text), &rejection); err != nil || status != 401 {
		t.Fatalf("%s %s: got %d %s, want the probe's 401", method, path, status, text)
	}
	if _, enveloped := rejection["ok"]; enveloped {
		t.Fatalf("%s %s: the daemon, not the probe, rejected the request: %s", method, path, text)
	}
}

func (f *fixture) mcp(t *testing.T, key string) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "hosted-connector-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: f.public.URL + "/mcp", HTTPClient: &http.Client{Transport: testTransport{key: key}}, DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	must(t, err)
	t.Cleanup(func() { session.Close() })
	return session
}

func call[T any](t *testing.T, session *mcp.ClientSession, name string, arguments any) T {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
	must(t, err)
	if result.IsError {
		t.Fatalf("%s failed: %+v", name, result.Content)
	}
	data, err := json.Marshal(result.StructuredContent)
	must(t, err)
	var value T
	must(t, json.Unmarshal(data, &value))
	return value
}

func (f *fixture) worker(t *testing.T, scope string) *bus.AgentSession {
	t.Helper()
	session, err := bus.StartAgentSession(context.Background(), bus.AgentSessionOptions{
		Address: f.public.URL + "/bus", ScopeToken: f.scopes[scope],
		HTTP:         &http.Client{Transport: testTransport{}, Timeout: 5 * time.Second},
		Registration: bus.RegisterAgentInput{ID: "worker", DisplayName: "Laptop agent", ConnectTo: []string{"muse"}},
	})
	must(t, err)
	t.Cleanup(func() { session.Close(context.Background()) })
	return session
}

func TestGatewayRoundTripAndScopeIsolation(t *testing.T) {
	f := setup(t, 2)
	worker := f.worker(t, "owner-0")
	other := f.worker(t, "owner-1")
	connector := f.mcp(t, f.keys[0])
	tools, err := connector.ListTools(context.Background(), &mcp.ListToolsParams{})
	must(t, err)
	if len(tools.Tools) != 20 {
		t.Fatalf("expected Bus tools, got %d", len(tools.Tools))
	}
	channel, err := worker.Client.CreateChannel(context.Background(), bus.CreateChannelInput{Name: "gateway"})
	must(t, err)
	must(t, worker.Client.SubscribeChannel(context.Background(), channel.ID, true))
	post := call[bus.ChannelPost](t, connector, "post_channel", bus.PostChannelInput{ChannelID: channel.ID, Body: "Shared update"})
	history, err := worker.Client.ChannelHistory(context.Background(), bus.ChannelHistoryInput{ChannelID: channel.ID})
	must(t, err)
	if len(history.Posts) != 1 || history.Posts[0].ID != post.ID {
		t.Fatal("channel history did not cross the gateway")
	}
	notifications, err := worker.Client.PullInbox(context.Background(), 10, 0)
	must(t, err)
	if len(notifications) != 1 {
		t.Fatal("channel notification did not cross the gateway")
	}
	_, err = worker.Client.AcknowledgeMessages(context.Background(), []string{notifications[0].ID})
	must(t, err)
	_, err = worker.Client.WakeStatus(context.Background())
	must(t, err)
	peers := call[struct{ Peers []bus.Agent }](t, connector, "list_peers", map[string]any{})
	if len(peers.Peers) != 1 || peers.Peers[0].ID != "worker" {
		t.Fatalf("wrong peers: %+v", peers)
	}
	arguments := map[string]any{"peer": "worker", "mode": "request", "message": "Review the checkout change", "idempotencyKey": "hosted-roundtrip"}
	receipt := call[bus.DeliveryReceipt](t, connector, "message_peer", arguments)
	retry := call[bus.DeliveryReceipt](t, connector, "message_peer", arguments)
	if receipt.MessageID == "" || receipt.MessageID != retry.MessageID {
		t.Fatal("retry created a different request")
	}
	messages, err := worker.Client.PullInbox(context.Background(), 10, 0)
	must(t, err)
	if len(messages) != 1 || messages[0].Body != "Review the checkout change" {
		t.Fatalf("laptop did not receive the request: %+v", messages)
	}
	foreign, err := other.Client.PullInbox(context.Background(), 10, 0)
	must(t, err)
	if len(foreign) != 0 {
		t.Fatal("request crossed scopes")
	}
	if _, err := other.Client.Receipt(context.Background(), receipt.MessageID); err == nil {
		t.Fatal("other scope read the receipt")
	}
	_, err = worker.Client.AcknowledgeMessages(context.Background(), []string{receipt.MessageID})
	must(t, err)
	_, err = worker.Client.SendMessage(context.Background(), bus.SendMessageInput{To: "muse", Body: "Review complete", Mode: bus.MessageResponse, ResponseTo: receipt.MessageID})
	must(t, err)
	replies := call[struct{ Messages []bus.Message }](t, connector, "check_inbox", map[string]any{})
	if len(replies.Messages) != 1 || replies.Messages[0].Body != "Review complete" {
		t.Fatalf("connector did not receive the reply: %+v", replies)
	}
	call[map[string]any](t, connector, "acknowledge_messages", map[string]any{"messageIds": []string{replies.Messages[0].ID}})
	final := call[bus.DeliveryReceipt](t, connector, "message_receipt", map[string]any{"messageId": receipt.MessageID})
	if final.ResponseMessageID != replies.Messages[0].ID {
		t.Fatal("reply was not correlated")
	}
}

func TestGatewayRejectsUntrustedRequestsAndPrivateRoutes(t *testing.T) {
	f := setup(t, 1)
	tests := []struct {
		name, method, path, key, host, origin string
		status                                int
		noBody                                bool
	}{
		{"missing key", "POST", "/mcp", "", "bus.example.test", "", 401, false},
		{"wrong key", "POST", "/mcp", strings.Repeat("z", 48), "bus.example.test", "", 401, false},
		{"scope token is not connector key", "POST", "/mcp", f.scopes["owner-0"], "bus.example.test", "", 401, false},
		{"wrong host", "POST", "/mcp", f.keys[0], "evil.example", "", 403, false},
		{"wrong origin", "POST", "/mcp", f.keys[0], "bus.example.test", "https://evil.example", 403, false},
		{"query key is not auth", "POST", "/mcp?access_token=" + f.keys[0], "", "bus.example.test", "", 401, false},
		{"admin blocked even with admin token", "POST", "/bus/v1/admin/shutdown", "private-admin-token", "bus.example.test", "", 404, false},
		{"scope creation blocked", "POST", "/bus/v1/scopes", "private-admin-token", "bus.example.test", "", 404, false},
		{"backup blocked", "GET", "/bus/v1/admin/backup", "private-admin-token", "bus.example.test", "", 404, false},
		{"pruning blocked", "POST", "/bus/v1/scope/storage/prune", f.scopes["owner-0"], "bus.example.test", "", 404, false},
		{"traversal blocked", "POST", "/bus/v1/tasks/../admin/shutdown", "private-admin-token", "bus.example.test", "", 404, false},
		{"encoded slash blocked", "POST", "/bus/v1%2fadmin/shutdown", "private-admin-token", "bus.example.test", "", 404, false},
		{"connector key cannot register agents", "POST", "/bus/v1/agents", f.keys[0], "bus.example.test", "", 401, false},
		{"connector key cannot use raw MCP", "POST", "/bus/mcp", f.keys[0], "bus.example.test", "", 401, false},
		{"ready", "GET", "/health/ready", "", "bus.example.test", "", 204, true},
		{"ready rejects a body", "GET", "/health/ready", "", "bus.example.test", "", 400, false},
		{"proxied health rejects a body", "GET", "/bus/health", "", "bus.example.test", "", 400, false},
		{"missing bearer takes no pool", "GET", "/bus/v1/peers", "", "bus.example.test", "", 401, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var body io.Reader = strings.NewReader(`{}`)
			if tc.noBody {
				body = nil
			}
			r := httptest.NewRequest(tc.method, tc.path, body)
			r.Host = tc.host
			if tc.key != "" {
				r.Header.Set("Authorization", "Bearer "+tc.key)
			}
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			f.g.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tc.status, w.Body.String())
			}
			if strings.Contains(w.Body.String(), f.keys[0]) || strings.Contains(w.Body.String(), f.scopes["owner-0"]) {
				t.Fatal("credential leaked in error response")
			}
			if w.Code >= 400 && w.Header().Get("Connection") != "close" {
				t.Fatal("rejection did not close the connection")
			}
			requirePoolsEmpty(t, f)
		})
	}
}

func TestGatewayRestartPreservesQueuedWorkAndRotatesKey(t *testing.T) {
	f := setup(t, 1)
	worker := f.worker(t, "owner-0")
	_, err := worker.Client.SendMessage(context.Background(), bus.SendMessageInput{To: "muse", Mode: bus.MessageNotify, Body: "Durable result"})
	must(t, err)
	oldExecutionToken := f.g.clients[0].session.Registration.AgentToken
	must(t, f.g.Close(context.Background()))
	if _, err := (bus.Client{Address: f.private.URL, Token: oldExecutionToken}).ListPeers(context.Background()); err == nil {
		t.Fatal("closed connector retained execution authority")
	}
	newKey := strings.Repeat("n", 48)
	digest := sha256.Sum256([]byte(newKey))
	f.config.Clients[0].APIKeySHA256 = hex.EncodeToString(digest[:])
	restarted, err := New(context.Background(), f.private.URL, f.config, func(scope string) (string, error) { return f.scopes[scope], nil })
	must(t, err)
	t.Cleanup(func() { restarted.Close(context.Background()) })
	public := httptest.NewServer(restarted)
	t.Cleanup(public.Close)
	f.public = public
	r, _ := http.NewRequest("POST", public.URL+"/mcp", strings.NewReader(`{}`))
	response, err := (&http.Client{Transport: testTransport{key: f.keys[0]}}).Do(r)
	must(t, err)
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("revoked connector key still accepted")
	}
	connector := f.mcp(t, newKey)
	replies := call[struct{ Messages []bus.Message }](t, connector, "check_inbox", map[string]any{})
	if len(replies.Messages) != 1 || replies.Messages[0].Body != "Durable result" {
		t.Fatal("gateway restart lost accepted work")
	}
}

func TestGatewaySessionFailureFailsClosed(t *testing.T) {
	f := setup(t, 1)
	must(t, f.g.clients[0].session.Close(context.Background()))
	select {
	case <-f.g.Done():
	case <-time.After(time.Second):
		t.Fatal("execution loss did not signal gateway shutdown")
	}
	for _, route := range []string{"/mcp", "/health/ready", "/bus/v1/agents"} {
		r := httptest.NewRequest("GET", route, nil)
		r.Host = "bus.example.test"
		r.Header.Set("Authorization", "Bearer "+f.keys[0])
		w := httptest.NewRecorder()
		f.g.ServeHTTP(w, r)
		if w.Code != 503 {
			t.Fatalf("failed gateway served %s: %d", route, w.Code)
		}
	}
}

func TestGatewayOneFailedConnectorDoesNotDisableOtherScopes(t *testing.T) {
	f := setup(t, 2)
	must(t, f.g.clients[0].session.Close(context.Background()))
	connector := f.mcp(t, f.keys[1])
	call[map[string]any](t, connector, "get_node_status", map[string]any{})
	r := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{}`))
	r.Host = "bus.example.test"
	r.Header.Set("Authorization", "Bearer "+f.keys[0])
	w := httptest.NewRecorder()
	f.g.ServeHTTP(w, r)
	if w.Code != 503 {
		t.Fatal("failed connector did not fail closed")
	}
	select {
	case <-f.g.Done():
		t.Fatal("one scope disabled other connectors")
	default:
	}
}

func TestGatewayRefusesDaemonWithoutCredentialRoute(t *testing.T) {
	f := setup(t, 1)
	target, err := url.Parse(f.private.URL)
	must(t, err)
	proxy := httputil.NewSingleHostReverseProxy(target)
	for _, tc := range []struct {
		name, code, want string
		status           int
	}{
		{"missing route", "NOT_FOUND", "upgrade the daemon", 404},
		{"probe failure", "INTERNAL", "credential probe for client 0", 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var registered atomic.Bool
			older := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/credential":
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tc.status)
					fmt.Fprintf(w, `{"ok":false,"error":{"code":%q,"message":"probe"}}`, tc.code)
				case "/v1/agents":
					registered.Store(true)
					fallthrough
				default:
					proxy.ServeHTTP(w, r)
				}
			}))
			t.Cleanup(older.Close)
			g, err := New(context.Background(), older.URL, f.config, func(scope string) (string, error) { return f.scopes[scope], nil })
			if g != nil {
				g.Close(context.Background())
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("gateway started against an incompatible daemon: %v", err)
			}
			if registered.Load() {
				t.Fatal("incompatible daemon check ran after a connector execution was displaced")
			}
		})
	}
}

func TestGatewayValidatesConfigurationBeforeRegistration(t *testing.T) {
	f := setup(t, 1)
	for _, change := range []func(*Config){
		func(c *Config) { c.PublicURL = "http://bus.example.test" },
		func(c *Config) { c.PublicURL += "/mcp" },
		func(c *Config) { c.Clients = nil },
		func(c *Config) { c.Clients = append(c.Clients, c.Clients[0]) },
		func(c *Config) { c.Clients[0].APIKeySHA256 = "weak" },
		func(c *Config) { c.Clients[0].Agent = "../escape" },
	} {
		config := f.config
		config.Clients = append([]ClientConfig{}, f.config.Clients...)
		change(&config)
		if _, err := New(context.Background(), f.private.URL, config, func(string) (string, error) {
			t.Fatal("invalid config reached credential resolution")
			return "", nil
		}); err == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
}

func TestGatewayConnectorReportsIdleAndReady(t *testing.T) {
	f := setup(t, 1)
	worker := f.worker(t, "owner-0")
	peers, err := worker.Client.ListPeers(context.Background())
	must(t, err)
	if len(peers) != 1 || peers[0].ID != "muse" || peers[0].Lifecycle != bus.LifecycleIdle || !peers[0].Ready {
		t.Fatalf("connector execution did not report idle/ready: %+v", peers)
	}
}

func TestGatewayConnectorAcceptsOnlyPost(t *testing.T) {
	f := setup(t, 1)
	for _, method := range []string{"GET", "DELETE", "PUT"} {
		r := httptest.NewRequest(method, "/mcp", nil)
		r.Host = "bus.example.test"
		r.Header.Set("Authorization", "Bearer "+f.keys[0])
		w := httptest.NewRecorder()
		f.g.ServeHTTP(w, r)
		if w.Code != 405 || w.Header().Get("Allow") != "POST" {
			t.Fatalf("%s /mcp: got %d Allow=%q, want 405 Allow=POST", method, w.Code, w.Header().Get("Allow"))
		}
	}
	for _, method := range []string{"GET", "DELETE"} {
		r := httptest.NewRequest(method, "/bus/mcp", nil)
		r.Host = "bus.example.test"
		w := httptest.NewRecorder()
		f.g.ServeHTTP(w, r)
		if w.Code != 404 {
			t.Fatalf("%s /bus/mcp is public: %d", method, w.Code)
		}
	}
}

func TestGatewayBudgetsRejectWithRetryAfterAndNeverStarveAuth(t *testing.T) {
	f := setup(t, 1)
	worker := f.registerWorker(t, "owner-0", "worker")
	heartbeat, err := json.Marshal(bus.HeartbeatInput{Lifecycle: bus.LifecycleIdle, Ready: true})
	must(t, err)
	serve := func(method, route, key, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, route, strings.NewReader(body))
		r.Host = "bus.example.test"
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		w := httptest.NewRecorder()
		f.g.ServeHTTP(w, r)
		return w
	}
	expect := func(name string, w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if w.Code != status {
			t.Fatalf("%s: got %d want %d (%s) body=%s", name, w.Code, status, f.pools(), w.Body.String())
		}
		if status == 429 && w.Header().Get("Retry-After") != "1" {
			t.Fatalf("%s: 429 without Retry-After", name)
		}
		if status >= 400 && w.Header().Get("Connection") != "close" {
			t.Fatalf("%s: rejection did not close the connection", name)
		}
	}
	fill := func(pool chan struct{}) func() {
		for i := 0; i < cap(pool); i++ {
			pool <- struct{}{}
		}
		return func() {
			for i := 0; i < cap(pool); i++ {
				<-pool
			}
		}
	}

	// Shared budget full: only validated non-control traffic is affected.
	drain := fill(f.g.budget)
	expect("missing bearer over full budget", serve("GET", "/bus/v1/peers", "", `{}`), 401)
	expect("worker peers over full budget", serve("GET", "/bus/v1/peers", worker.Token, ``), 429)
	expect("worker heartbeat over full budget", serve("PATCH", "/bus/v1/me/heartbeat", worker.Token, string(heartbeat)), 200)
	expect("liveness", serve("GET", "/health/live", "", ``), 204)
	expect("invalid connector key", serve("POST", "/mcp", "not-a-valid-key-but-long-enough-to-pass-length-checks", `{}`), 401)
	expect("valid connector key over full budget", serve("POST", "/mcp", f.keys[0], `{}`), 429)
	drain()

	// Control full: heartbeats are rejected, everything else proceeds.
	drain = fill(f.g.control)
	expect("heartbeat over full control", serve("PATCH", "/bus/v1/me/heartbeat", worker.Token, string(heartbeat)), 429)
	expect("missing bearer over full control", serve("PATCH", "/bus/v1/me/heartbeat", "", string(heartbeat)), 401)
	expect("worker peers over full control", serve("GET", "/bus/v1/peers", worker.Token, ``), 200)
	drain()

	// Admission full: nothing that needs a probe is admitted, including a
	// valid heartbeat. This documents the limit; it is not a survival claim.
	drain = fill(f.g.admission)
	expect("missing bearer over full admission", serve("GET", "/bus/v1/peers", "", ``), 401)
	expect("invalid bearer over full admission", serve("GET", "/bus/v1/peers", "invalid-token", ``), 429)
	expect("readiness over full admission", serve("GET", "/health/ready", "", ``), 429)
	expect("liveness over full admission", serve("GET", "/health/live", "", ``), 204)
	expect("valid heartbeat over full admission", serve("PATCH", "/bus/v1/me/heartbeat", worker.Token, string(heartbeat)), 429)
	drain()

	// Per-connector budget is independent of the shared one.
	drain = fill(f.g.clients[0].budget)
	expect("connector budget", serve("POST", "/mcp", f.keys[0], `{}`), 429)
	expect("worker peers over full connector budget", serve("GET", "/bus/v1/peers", worker.Token, ``), 200)
	drain()
	// The daemon rejects the missing Content-Type with 415; the point is that
	// no gateway pool rejected it.
	if w := serve("POST", "/mcp", f.keys[0], `{}`); w.Code != 415 {
		t.Fatalf("connector after drain: got %d want the daemon's 415 (%s)", w.Code, f.pools())
	}
	requirePoolsEmpty(t, f)
}

func TestGatewayReportsBadGatewayWhenDaemonIsDown(t *testing.T) {
	f := setup(t, 1)
	worker := f.registerWorker(t, "owner-0", "worker")
	// Stop accepting connections without ending the registered execution, so
	// the proxy path (not the fail-closed path) is what answers.
	must(t, f.private.Listener.Close())
	f.private.CloseClientConnections()
	select {
	case <-f.g.Done():
		t.Fatal("connector session ended before the assertions; the fail-closed path would answer instead")
	default:
	}
	r := httptest.NewRequest("GET", "/bus/v1/peers", nil)
	r.Host = "bus.example.test"
	r.Header.Set("Authorization", "Bearer "+worker.Token)
	w := httptest.NewRecorder()
	f.g.ServeHTTP(w, r)
	if w.Code != 502 || w.Header().Get("Connection") != "close" {
		t.Fatalf("daemon down should be 502 from the credential probe with Connection: close, got %d", w.Code)
	}
	requirePoolsEmpty(t, f)
	r = httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	r.Host = "bus.example.test"
	r.Header.Set("Authorization", "Bearer "+f.keys[0])
	w = httptest.NewRecorder()
	f.g.ServeHTTP(w, r)
	if w.Code != 502 || w.Header().Get("Connection") != "close" {
		t.Fatalf("daemon down should be 502 from the proxy with Connection: close, got %d", w.Code)
	}
	r = httptest.NewRequest("GET", "/health/ready", nil)
	r.Host = "bus.example.test"
	w = httptest.NewRecorder()
	f.g.ServeHTTP(w, r)
	if w.Code != 503 {
		t.Fatalf("readiness with daemon down should be 503, got %d", w.Code)
	}
}

func TestGatewayForwardsBodylessHealthProbes(t *testing.T) {
	f := setup(t, 1)
	for _, route := range []string{"/bus/health", "/bus/health/live", "/bus/health/ready"} {
		status, body := f.request(t, "GET", route, "", nil)
		if status != 200 || !strings.Contains(body, `"name":"october-bus"`) {
			t.Fatalf("%s: got %d %s, want the daemon's health object", route, status, body)
		}
	}
	requirePoolsEmpty(t, f)
}

func TestGatewayMapsDaemonBackpressureOnProbeToRetryAfter(t *testing.T) {
	var throttled atomic.Value
	throttled.Store("")
	f := setupWith(t, 1, ":memory:", func(daemon string) string {
		target, err := url.Parse(daemon)
		must(t, err)
		proxy := httputil.NewSingleHostReverseProxy(target)
		wrapper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/credential" && r.Header.Get("Authorization") == "Bearer "+throttled.Load().(string) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(http.StatusTooManyRequests)
				io.WriteString(w, `{"ok":false,"error":{"code":"BACKPRESSURE","message":"Concurrent request limit is full"}}`)
				return
			}
			proxy.ServeHTTP(w, r)
		}))
		t.Cleanup(wrapper.Close)
		return wrapper.URL
	})
	worker := f.registerWorker(t, "owner-0", "worker")
	throttled.Store(worker.Token)
	r, err := http.NewRequest("GET", f.public.URL+"/bus/v1/peers", nil)
	must(t, err)
	response, err := (&http.Client{Transport: testTransport{key: worker.Token}, Timeout: 5 * time.Second}).Do(r)
	must(t, err)
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 429 || response.Header.Get("Retry-After") != "1" || !response.Close || !strings.Contains(string(body), "Bus request limit reached") {
		t.Fatalf("daemon backpressure on the probe: got %d Retry-After=%q close=%v %s", response.StatusCode, response.Header.Get("Retry-After"), response.Close, body)
	}
	requirePoolsEmpty(t, f)
	throttled.Store("")
	if _, err := worker.ListPeers(context.Background()); err != nil {
		t.Fatalf("request after backpressure cleared failed: %v", err)
	}
}

// heldBody records whether anything read it and blocks every read until it is
// released, so a test can prove a handler never touched the client body.
type heldBody struct {
	read    atomic.Bool
	release chan struct{}
	once    sync.Once
}

func newHeldBody() *heldBody { return &heldBody{release: make(chan struct{})} }

func (b *heldBody) Read([]byte) (int, error) {
	b.read.Store(true)
	<-b.release
	return 0, io.EOF
}

func (b *heldBody) Close() error { return nil }
func (b *heldBody) Release()     { b.once.Do(func() { close(b.release) }) }

// serveHeld runs ServeHTTP against a request whose body is withheld. If the
// gateway is still waiting at the deadline, the body is released so the
// handler can finish, and the test fails.
func serveHeld(t *testing.T, g *Gateway, r *http.Request, body *heldBody) *httptest.ResponseRecorder {
	t.Helper()
	t.Cleanup(body.Release)
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		g.ServeHTTP(w, r)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		body.Release()
		<-done
		t.Fatalf("%s %s waited for a withheld body", r.Method, r.URL.Path)
	}
	return w
}

func TestGatewayRejectsInvalidCredentialsWithoutReadingBody(t *testing.T) {
	f := setup(t, 1)
	tests := []struct {
		name, method, path, key string
		length                  int64
		chunked                 bool
		status                  int
	}{
		{"registration with declared body", "POST", "/bus/v1/agents", "invalid-scope-token", 100, false, 401},
		{"heartbeat with chunked body", "PATCH", "/bus/v1/me/heartbeat", "invalid-agent-token", -1, true, 401},
		{"retirement with unknown token", "POST", "/bus/v1/me/retire", "unknown-agent-token", 100, false, 401},
		{"health probe with body", "GET", "/bus/health", "", 1, false, 400},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := newHeldBody()
			r := httptest.NewRequest(tc.method, tc.path, body)
			r.Host = "bus.example.test"
			r.ContentLength = tc.length
			if tc.chunked {
				r.TransferEncoding = []string{"chunked"}
			}
			if tc.key != "" {
				r.Header.Set("Authorization", "Bearer "+tc.key)
			}
			w := serveHeld(t, f.g, r, body)
			if w.Code != tc.status {
				t.Fatalf("got %d want %d: %s", w.Code, tc.status, w.Body.String())
			}
			if w.Header().Get("Connection") != "close" {
				t.Fatal("rejection did not close the connection")
			}
			if tc.status == 401 && w.Header().Get("WWW-Authenticate") == "" {
				t.Fatal("401 without a challenge")
			}
			if body.read.Load() {
				t.Fatal("gateway read the body of a rejected request")
			}
			requirePoolsEmpty(t, f)
		})
	}
}

// rawRequest renders one HTTP/1.1 request head; the body is never sent.
func rawRequest(method, path string, headers ...string) string {
	lines := append([]string{method + " " + path + " HTTP/1.1", "Host: bus.example.test"}, headers...)
	return strings.Join(lines, "\r\n") + "\r\n\r\n"
}

// openRaw dials the public listener, writes one request head, and registers
// the socket for cleanup. Every read and write is bounded by ctx's deadline.
// The server drains a withheld body on the connection goroutine after the
// rejection, and httptest.Server.Close waits for it, so the socket must be
// closed before the fixture's public server: register it after setup, and
// close it explicitly when the test is done with it.
func openRaw(t *testing.T, ctx context.Context, addr net.Addr, raw string) net.Conn {
	t.Helper()
	conn, err := (&net.Dialer{}).DialContext(ctx, addr.Network(), addr.String())
	must(t, err)
	t.Cleanup(func() { conn.Close() })
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("raw sockets need a context deadline")
	}
	must(t, conn.SetDeadline(deadline))
	_, err = io.WriteString(conn, raw)
	must(t, err)
	return conn
}

// readHead returns the response head without reading its body.
func readHead(t *testing.T, conn net.Conn) *http.Response {
	t.Helper()
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("no response head before the deadline: %v", err)
	}
	return response
}

// httptest servers have no ReadTimeout, so a rejection that drained the
// withheld body before flushing would hang here until the deadline.
func TestGatewayRejectsWithheldBodiesPromptlyOverTCP(t *testing.T) {
	f := setup(t, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	addr := f.public.Listener.Addr()
	for _, tc := range []struct {
		name, raw string
		status    int
	}{
		{"declared body", rawRequest("POST", "/bus/v1/agents", "Authorization: Bearer invalid-scope-token", "Content-Type: application/json", "Content-Length: 100"), 401},
		{"chunked body", rawRequest("POST", "/bus/v1/agents", "Authorization: Bearer invalid-scope-token", "Content-Type: application/json", "Transfer-Encoding: chunked"), 401},
		{"health with body", rawRequest("GET", "/bus/health", "Content-Length: 1"), 400},
		{"health with chunked body", rawRequest("GET", "/bus/health", "Transfer-Encoding: chunked"), 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := openRaw(t, ctx, addr, tc.raw)
			response := readHead(t, conn)
			// ReadResponse folds a Connection: close header into Close.
			if response.StatusCode != tc.status || !response.Close {
				t.Fatalf("got %d close=%v, want %d and a closed connection", response.StatusCode, response.Close, tc.status)
			}
			requirePoolsEmpty(t, f)
			conn.Close()
		})
	}
}

// The burst is answered immediately, so this proves the connector and a new
// worker are unaffected after it; the held-probe test covers "while".
func TestGatewayServesConnectorAfterInvalidBurst(t *testing.T) {
	f := setup(t, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	addr := f.public.Listener.Addr()
	burst := cap(f.g.budget) + cap(f.g.admission) + cap(f.g.control)
	conns := make([]net.Conn, 0, burst)
	for i := 0; i < burst; i++ {
		raw := rawRequest("POST", "/bus/v1/agents", fmt.Sprintf("Authorization: Bearer invalid-%d", i), "Content-Type: application/json", "Content-Length: 100")
		conns = append(conns, openRaw(t, ctx, addr, raw))
	}
	connector := f.mcp(t, f.keys[0])
	tools, err := connector.ListTools(ctx, &mcp.ListToolsParams{})
	must(t, err)
	if len(tools.Tools) == 0 {
		t.Fatal("connector served no tools during the burst")
	}
	counts := map[int]int{}
	for _, conn := range conns {
		response := readHead(t, conn)
		counts[response.StatusCode]++
		if response.StatusCode != 401 && response.StatusCode != 429 {
			t.Fatalf("burst request was answered with %d", response.StatusCode)
		}
		if !response.Close {
			t.Fatal("burst rejection did not close the connection")
		}
	}
	t.Logf("burst of %d withheld bodies answered: %v", burst, counts)
	if counts[401] == 0 {
		t.Fatal("no burst request reached the credential probe")
	}
	requirePoolsEmpty(t, f)
	for _, conn := range conns {
		conn.Close()
	}
	worker := f.worker(t, "owner-0")
	if _, err := worker.SetState(ctx, bus.LifecycleReady, true); err != nil {
		t.Fatalf("heartbeat after the burst failed: %v", err)
	}
	r := httptest.NewRequest("GET", "/health/ready", nil)
	r.Host = "bus.example.test"
	w := httptest.NewRecorder()
	f.g.ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatalf("readiness after the burst: %d", w.Code)
	}
}

func TestGatewayHeartbeatsSurviveWhileProbesAreHeld(t *testing.T) {
	hold := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(hold) }) }
	arrived := make(chan struct{}, 64)
	// A loopback wrapper in front of the daemon parks credential probes for
	// invalid bearers and reverse-proxies everything else untouched.
	f := setupWith(t, 1, ":memory:", func(daemon string) string {
		target, err := url.Parse(daemon)
		must(t, err)
		proxy := httputil.NewSingleHostReverseProxy(target)
		wrapper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/credential" && strings.HasPrefix(r.Header.Get("Authorization"), "Bearer invalid-") {
				arrived <- struct{}{}
				select {
				case <-hold:
				case <-r.Context().Done():
					return
				}
			}
			proxy.ServeHTTP(w, r)
		}))
		t.Cleanup(wrapper.Close)
		return wrapper.URL
	})
	t.Cleanup(release)
	// The parked probes must outlive everything asserted while they are held.
	f.g.probeTimeout = 30 * time.Second
	worker := f.worker(t, "owner-0")
	const held = 8
	results := make(chan int, held)
	for i := 0; i < held; i++ {
		go func(key string) {
			r, err := http.NewRequest("GET", f.public.URL+"/bus/v1/peers", nil)
			if err != nil {
				results <- -1
				return
			}
			response, err := (&http.Client{Transport: testTransport{key: key}, Timeout: 20 * time.Second}).Do(r)
			if err != nil {
				results <- -1
				return
			}
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			results <- response.StatusCode
		}(fmt.Sprintf("invalid-%d", i))
	}
	for i := 0; i < held; i++ {
		select {
		case <-arrived:
		case <-time.After(10 * time.Second):
			t.Fatalf("only %d of %d probes reached the daemon", i, held)
		}
	}
	// The held probes occupy admission only. Valid control traffic, readiness,
	// and the connector proceed while they are parked.
	requirePools(t, f, held, 0, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := worker.SetState(ctx, bus.LifecycleWorking, true); err != nil {
		t.Fatalf("heartbeat failed while probes were held: %v", err)
	}
	r := httptest.NewRequest("GET", "/health/ready", nil)
	r.Host = "bus.example.test"
	w := httptest.NewRecorder()
	f.g.ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatalf("readiness while probes were held: %d", w.Code)
	}
	connector := f.mcp(t, f.keys[0])
	call[map[string]any](t, connector, "get_node_status", map[string]any{})
	release()
	for i := 0; i < held; i++ {
		select {
		case status := <-results:
			if status != 401 {
				t.Fatalf("released probe answered %d, want 401", status)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("released probes did not complete")
		}
	}
	requirePoolsEmpty(t, f)
}

func TestGatewayAdmitsEveryDaemonCredentialKindAndRetirement(t *testing.T) {
	ctx := context.Background()
	heartbeat := bus.HeartbeatInput{Lifecycle: bus.LifecycleIdle, Ready: true}
	t.Run("output principal publishes", func(t *testing.T) {
		f := setup(t, 1)
		scope := f.scopes["owner-0"]
		stream, err := f.runtime.CreateOutputStream(ctx, scope, bus.CreateOutputStreamInput{Name: "gateway"})
		must(t, err)
		principal, err := f.runtime.CreateOutputPrincipal(ctx, scope, bus.CreateOutputPrincipalInput{
			StreamID: stream.ID, Label: "Build service", Permissions: []bus.OutputPermission{bus.OutputPublish},
		})
		must(t, err)
		_, err = f.busClient(principal.Credential).PublishOutput(ctx, stream.ID, bus.PublishOutputInput{ContentType: bus.OutputText, Value: "published through the gateway"})
		must(t, err)
	})
	t.Run("scope token rotation", func(t *testing.T) {
		// Rotation retires the scope's connector execution; a second scope
		// keeps the gateway serving.
		f := setup(t, 2)
		old := f.scopes["owner-1"]
		_, err := f.busClient(old).ListAgents(ctx)
		must(t, err)
		rotated, err := f.runtime.RotateScopeToken(ctx, "owner-1")
		must(t, err)
		requireProbeRejected(t, f, "GET", "/bus/v1/agents", old, nil)
		_, err = f.busClient(rotated.ScopeToken).ListAgents(ctx)
		must(t, err)
	})
	t.Run("retirement repeats and revokes", func(t *testing.T) {
		f := setup(t, 1)
		worker := f.registerWorker(t, "owner-0", "worker")
		must(t, worker.Retire(ctx))
		must(t, worker.Retire(ctx))
		requireProbeRejected(t, f, "PATCH", "/bus/v1/me/heartbeat", worker.Token, heartbeat)
	})
	t.Run("replaced token cannot retire", func(t *testing.T) {
		f := setup(t, 1)
		replaced := f.registerWorker(t, "owner-0", "worker")
		f.registerWorker(t, "owner-0", "worker")
		requireProbeRejected(t, f, "POST", "/bus/v1/me/retire", replaced.Token, struct{}{})
	})
	t.Run("expired lease may retire but not heartbeat", func(t *testing.T) {
		source := filepath.Join(t.TempDir(), "bus.db")
		f := setupWith(t, 1, source, nil)
		worker := f.registerWorker(t, "owner-0", "worker")
		// The daemon's connection holds the write lock during its fsync; wait
		// for it instead of failing with SQLITE_BUSY.
		db, err := sql.Open("sqlite", source+"?_pragma=busy_timeout(2500)")
		must(t, err)
		defer db.Close()
		result, err := db.ExecContext(ctx, `UPDATE agents SET lease_expires_at=? WHERE scope_id=? AND agent_id=?`, time.Now().Add(-time.Second).UnixMilli(), "owner-0", "worker")
		must(t, err)
		if rows, _ := result.RowsAffected(); rows != 1 {
			t.Fatalf("expired %d executions, want 1", rows)
		}
		requireProbeRejected(t, f, "PATCH", "/bus/v1/me/heartbeat", worker.Token, heartbeat)
		must(t, worker.Retire(ctx))
		must(t, worker.Retire(ctx))
	})
}
