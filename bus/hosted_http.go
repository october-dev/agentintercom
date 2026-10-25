package bus

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-oauth2/oauth2/v4/server"
	"golang.org/x/time/rate"
)

type HostedServerOptions struct {
	PublicURL      string
	AllowedOrigins []string
	Identity       HostedIdentityVerifier
}

type HostedServer struct {
	accounts           *HostedAccounts
	identity           HostedIdentityVerifier
	core               *Server
	routes             *http.ServeMux
	publicURL          string
	host               string
	origins            map[string]bool
	requests           chan struct{}
	oauth              *server.Server
	oauthRegistration  *rate.Limiter
	oauthAuthorization *rate.Limiter
	oauthRequests      chan struct{}
}

func NewHostedServer(accounts *HostedAccounts, options HostedServerOptions) (*HostedServer, error) {
	u, err := url.Parse(options.PublicURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || options.Identity == nil || accounts == nil {
		return nil, errors.New("hosted service requires an HTTPS public origin, accounts and identity verifier")
	}
	h := &HostedServer{accounts: accounts, identity: options.Identity, routes: http.NewServeMux(), publicURL: strings.TrimRight(options.PublicURL, "/"), host: u.Host, origins: map[string]bool{}, requests: make(chan struct{}, 64)}
	h.origins[h.publicURL] = true
	for _, origin := range options.AllowedOrigins {
		v, err := url.Parse(origin)
		if err != nil || v.Scheme != "https" || v.Host == "" || v.User != nil || v.Path != "" || v.RawQuery != "" || v.Fragment != "" {
			return nil, errors.New("allowed browser origins must be exact HTTPS origins")
		}
		h.origins[origin] = true
	}
	h.core = NewServer(accounts.runtime, ServerOptions{PublicBaseURL: h.publicURL, AllowedHosts: []string{h.host}, AllowedOrigins: append([]string{h.publicURL}, options.AllowedOrigins...)})
	h.routes.HandleFunc("GET /api/workspaces", h.userRoute(func(ctx context.Context, user HostedUser, w http.ResponseWriter, r *http.Request) (any, error) {
		return accounts.ListWorkspaces(ctx, user)
	}))
	h.routes.HandleFunc("POST /api/workspaces", h.userRoute(func(ctx context.Context, user HostedUser, w http.ResponseWriter, r *http.Request) (any, error) {
		var input struct {
			Name string `json:"name"`
		}
		if err := decodeBody(w, r, &input); err != nil {
			return nil, err
		}
		return accounts.CreateWorkspace(ctx, user, input.Name)
	}))
	h.routes.HandleFunc("GET /api/workspaces/{workspace}/members", h.userRoute(func(ctx context.Context, user HostedUser, w http.ResponseWriter, r *http.Request) (any, error) {
		return accounts.Members(ctx, user, r.PathValue("workspace"))
	}))
	h.routes.HandleFunc("PATCH /api/workspaces/{workspace}/members/{user}", h.userRoute(func(ctx context.Context, user HostedUser, w http.ResponseWriter, r *http.Request) (any, error) {
		var input struct {
			Role string `json:"role"`
		}
		if err := decodeBody(w, r, &input); err != nil {
			return nil, err
		}
		if input.Role == "" {
			return nil, Errorf(CodeInvalidArgument, "role is required; use DELETE to remove a member")
		}
		return map[string]bool{"updated": true}, accounts.ChangeMember(ctx, user, r.PathValue("workspace"), r.PathValue("user"), input.Role)
	}))
	h.routes.HandleFunc("DELETE /api/workspaces/{workspace}/members/{user}", h.userRoute(func(ctx context.Context, user HostedUser, w http.ResponseWriter, r *http.Request) (any, error) {
		return map[string]bool{"removed": true}, accounts.ChangeMember(ctx, user, r.PathValue("workspace"), r.PathValue("user"), "")
	}))
	h.routes.HandleFunc("POST /api/workspaces/{workspace}/invites", h.userRoute(func(ctx context.Context, user HostedUser, w http.ResponseWriter, r *http.Request) (any, error) {
		var input struct {
			Email string `json:"email"`
			Role  string `json:"role"`
		}
		if err := decodeBody(w, r, &input); err != nil {
			return nil, err
		}
		if input.Role == "" {
			input.Role = "member"
		}
		return accounts.Invite(ctx, user, r.PathValue("workspace"), input.Email, input.Role)
	}))
	h.routes.HandleFunc("POST /api/invites/accept", h.userRoute(func(ctx context.Context, user HostedUser, w http.ResponseWriter, r *http.Request) (any, error) {
		var input struct {
			Token string `json:"token"`
		}
		if err := decodeBody(w, r, &input); err != nil {
			return nil, err
		}
		id, err := accounts.AcceptInvite(ctx, user, input.Token)
		return map[string]string{"workspaceId": id}, err
	}))
	h.routes.HandleFunc("GET /api/workspaces/{workspace}/devices", h.userRoute(func(ctx context.Context, user HostedUser, w http.ResponseWriter, r *http.Request) (any, error) {
		return accounts.Devices(ctx, user, r.PathValue("workspace"))
	}))
	h.routes.HandleFunc("POST /api/workspaces/{workspace}/devices", h.userRoute(func(ctx context.Context, user HostedUser, w http.ResponseWriter, r *http.Request) (any, error) {
		var input struct {
			Name string `json:"name"`
		}
		if err := decodeBody(w, r, &input); err != nil {
			return nil, err
		}
		return accounts.EnrollDevice(ctx, user, r.PathValue("workspace"), input.Name)
	}))
	h.routes.HandleFunc("DELETE /api/workspaces/{workspace}/devices/{device}", h.userRoute(func(ctx context.Context, user HostedUser, w http.ResponseWriter, r *http.Request) (any, error) {
		return map[string]bool{"revoked": true}, accounts.RevokeDevice(ctx, user, r.PathValue("workspace"), r.PathValue("device"))
	}))
	h.routes.HandleFunc("POST /api/device/sessions", func(w http.ResponseWriter, r *http.Request) {
		token, err := bearer(r)
		if err != nil {
			writeFailure(w, err)
			return
		}
		var input struct {
			Name     string `json:"name"`
			ResumeID string `json:"resumeId"`
		}
		if err := decodeBody(w, r, &input); err != nil {
			writeFailure(w, err)
			return
		}
		result, err := accounts.StartDeviceSession(r.Context(), token, input.Name, input.ResumeID)
		if err != nil {
			writeFailure(w, err)
			return
		}
		writeResult(w, http.StatusOK, result)
	})
	h.oauthRegistration = rate.NewLimiter(1, 30)
	h.oauthAuthorization = rate.NewLimiter(5, 50)
	h.oauthRequests = make(chan struct{}, 32)
	h.registerOAuthRoutes()
	h.registerWorkspaceViews()
	h.registerBrowserRoutes()
	h.registerWorkRoutes()
	return h, nil
}

func (h *HostedServer) userRoute(fn func(context.Context, HostedUser, http.ResponseWriter, *http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, err := bearer(r)
		if err != nil {
			writeFailure(w, err)
			return
		}
		user, err := h.identity.Authenticate(r.Context(), token)
		if err != nil {
			writeFailure(w, err)
			return
		}
		if err := validateHostedUser(user); err != nil {
			writeFailure(w, err)
			return
		}
		value, err := fn(r.Context(), user, w, r)
		if err != nil {
			writeFailure(w, err)
			return
		}
		writeResult(w, http.StatusOK, value)
	}
}

func (h *HostedServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.Host != h.host {
		writeFailure(w, Errorf(CodePermissionDenied, "Request host is not allowed"))
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		if !h.origins[origin] && !(h.publicOAuthPath(r.URL.Path) && validOAuthBrowserOrigin(origin)) {
			writeFailure(w, Errorf(CodePermissionDenied, "Browser origin is not allowed"))
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Add("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, MCP-Protocol-Version, MCP-Session-Id")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
	}
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/admin/") || r.URL.Path == "/v1/scopes" {
		writeFailure(w, Errorf(CodeNotFound, "Route not found"))
		return
	}
	if h.serveBrowser(w, r) {
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/oauth/") || strings.HasPrefix(r.URL.Path, "/.well-known/") {
		select {
		case h.requests <- struct{}{}:
			defer func() { <-h.requests }()
		default:
			w.Header().Set("Retry-After", "1")
			writeFailure(w, Errorf(CodeBackpressure, "Service is busy; retry shortly"))
			return
		}
		h.routes.ServeHTTP(w, r)
		return
	}
	if r.URL.Path == "/mcp" {
		token, err := bearer(r)
		if err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+h.publicURL+`/.well-known/oauth-protected-resource/mcp"`)
			writeFailure(w, err)
			return
		}
		if strings.HasPrefix(token, "oba_") {
			execution, err := h.oauthExecution(r.Context(), token)
			if err != nil {
				if AsBusError(err).Code == CodeUnauthenticated {
					w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token", resource_metadata="`+h.publicURL+`/.well-known/oauth-protected-resource/mcp"`)
				}
				writeFailure(w, err)
				return
			}
			r = r.Clone(r.Context())
			r.Header.Set("Authorization", "Bearer "+execution)
			// Host is pinned above and the OAuth grant is checked before dispatch.
			// Origin policy is owned by this front door, not the local MCP server.
			r.Header.Del("Origin")
		}
	}
	h.core.ServeHTTP(w, r)
}

func (h *HostedServer) publicOAuthPath(path string) bool {
	return path == "/mcp" || strings.HasPrefix(path, "/oauth/") || strings.HasPrefix(path, "/.well-known/")
}
func validOAuthBrowserOrigin(origin string) bool {
	u, err := url.Parse(origin)
	return err == nil && validOAuthRedirect(origin) && u.Path == "" && u.RawQuery == "" && u.Fragment == ""
}
