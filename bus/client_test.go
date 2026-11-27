package bus

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestClientListPeersResponses(t *testing.T) {
	for _, tt := range []struct {
		name      string
		status    int
		body      string
		wantError string
		wantCode  ErrorCode
	}{
		{"html", 503, "<html>private proxy details</html>", "Agent Intercom request failed with HTTP 503", ""},
		{"empty", 502, "", "Agent Intercom request failed with HTTP 502", ""},
		{"malformed", 500, `{"private":`, "Agent Intercom request failed with HTTP 500", ""},
		{"protocol error", 403, `{"ok":false,"error":{"code":"PERMISSION_DENIED","message":"Access denied"}}`, "Access denied", CodePermissionDenied},
		{"success", 200, `{"ok":true,"result":[{"id":"peer-1"}]}`, "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			peers, err := (Client{Address: server.URL}).ListPeers(t.Context())
			if tt.wantError != "" {
				if err == nil || err.Error() != tt.wantError {
					t.Fatalf("error = %v, want %q", err, tt.wantError)
				}
				if tt.wantCode != "" {
					var busErr *BusError
					if !errors.As(err, &busErr) || busErr.Code != tt.wantCode {
						t.Fatalf("error = %#v, want code %s", err, tt.wantCode)
					}
				}
			} else if err != nil || len(peers) != 1 || peers[0].ID != "peer-1" {
				t.Fatalf("peers = %#v, error = %v", peers, err)
			}
		})
	}
}

func TestClientListPeersMalformedSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not JSON"))
	}))
	defer server.Close()
	_, err := (Client{Address: server.URL}).ListPeers(t.Context())
	var syntaxErr *json.SyntaxError
	if !errors.As(err, &syntaxErr) {
		t.Fatalf("error = %v, want JSON syntax error", err)
	}
}

func TestClientUploadAttachmentResponses(t *testing.T) {
	for _, tt := range []struct {
		name      string
		status    int
		body      string
		wantError string
		wantCode  ErrorCode
	}{
		{"html", 413, "<html>private proxy details</html>", "attachment upload failed with HTTP 413", ""},
		{"empty", 502, "", "attachment upload failed with HTTP 502", ""},
		{"protocol error", 403, `{"ok":false,"error":{"code":"PERMISSION_DENIED","message":"Access denied"}}`, "Access denied", CodePermissionDenied},
		{"malformed success", 200, "not JSON", "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			_, err := (Client{Address: server.URL, Token: "token"}).UploadAttachment(t.Context(), UploadAttachmentInput{Name: "file.bin"}, []byte("data"))
			if tt.wantError != "" {
				if err == nil || err.Error() != tt.wantError {
					t.Fatalf("error = %v, want %q", err, tt.wantError)
				}
				if tt.wantCode != "" {
					var busErr *BusError
					if !errors.As(err, &busErr) || busErr.Code != tt.wantCode {
						t.Fatalf("error = %#v, want code %s", err, tt.wantCode)
					}
				}
			} else {
				var syntaxErr *json.SyntaxError
				if !errors.As(err, &syntaxErr) {
					t.Fatalf("error = %v, want JSON syntax error", err)
				}
			}
		})
	}
}

func TestPullInboxReleasesReservationWhenCommitFails(t *testing.T) {
	var released atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/inbox/reserve":
			w.Write([]byte(`{"ok":true,"result":{"id":"res_1","messages":[]}}`))
		case "/v1/inbox/res_1/commit":
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"ok":false,"error":{"code":"UNAVAILABLE","message":"try again"}}`))
		case "/v1/inbox/res_1/release":
			released.Store(true)
			w.Write([]byte(`{"ok":true,"result":{}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	_, err := (Client{Address: server.URL, Token: "agent"}).PullInbox(context.Background(), 10, 0)
	if err == nil {
		t.Fatal("failed commit was reported as success")
	}
	if !released.Load() {
		t.Fatal("reservation was not released after a failed commit")
	}
}
