package bus

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type sessionTransport func(*http.Request) (*http.Response, error)

func (f sessionTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSessionRejectsIncompatibleRuntimeBeforeRegistration(t *testing.T) {
	for _, health := range []Health{
		{Name: "october-bus", ProtocolVersion: ProtocolVersion, Status: "ready"},
		{Name: "october-bus", ProtocolVersion: "future", Status: "ready", Features: []string{FeatureSessionRetirement}},
	} {
		calls := 0
		client := &http.Client{Transport: sessionTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			if r.URL.Path != "/health" || r.Header.Get("Authorization") != "" {
				t.Fatal("compatibility check registered or sent credentials")
			}
			body, _ := json.Marshal(health)
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
		})}
		_, err := StartAgentSession(context.Background(), AgentSessionOptions{Address: "http://session.invalid", ScopeToken: "synthetic", HTTP: client})
		if AsBusError(err).Code != CodeConflict || calls != 1 {
			t.Fatalf("err=%v calls=%d", err, calls)
		}
	}
}

func TestSessionFailedStateWritePreservesConfirmedState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	confirmed := time.Now().Add(time.Minute)
	session := &AgentSession{context: ctx, lifecycle: LifecycleReady, ready: true, leaseMS: 300000, leaseExpiresAt: confirmed}
	session.Client = Client{Address: "http://session.invalid", HTTP: &http.Client{Transport: sessionTransport(func(*http.Request) (*http.Response, error) {
		return nil, Errorf(CodeInternal, "rejected heartbeat")
	})}}
	_, err := session.SetState(ctx, LifecycleWorking, false)
	if err == nil || session.lifecycle != LifecycleReady || !session.ready {
		t.Fatalf("failed heartbeat changed confirmed state: %v", err)
	}
	if !session.LeaseExpiresAt().Equal(confirmed) {
		t.Fatal("failed write extended confirmed lease")
	}
	_, err = session.sendHeartbeat(ctx)
	if err == nil || !session.LeaseExpiresAt().Equal(confirmed) {
		t.Fatal("failed heartbeat extended confirmed lease")
	}
	session.closed = true
	if !session.LeaseExpiresAt().IsZero() {
		t.Fatal("closed session still advertised authority")
	}
}
