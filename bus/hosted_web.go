package bus

import (
	"context"
	"embed"
	"net/http"
	"strings"
)

//go:embed hostedweb/*
var hostedWeb embed.FS

func (h *HostedServer) registerBrowserRoutes() {
	h.routes.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
		config := map[string]any{"mcpUrl": h.publicURL + "/mcp"}
		if identity, ok := h.identity.(*SupabaseIdentity); ok {
			config["authUrl"] = identity.origin
			config["authPublicKey"] = identity.key
		}
		writeResult(w, http.StatusOK, config)
	})
	h.routes.HandleFunc("GET /api/me", h.userRoute(func(_ context.Context, user HostedUser, _ http.ResponseWriter, _ *http.Request) (any, error) {
		return user, nil
	}))
}

func (h *HostedServer) serveBrowser(w http.ResponseWriter, r *http.Request) bool {
	file, contentType := "", ""
	switch r.URL.Path {
	case "/", "/app", "/app/":
		file, contentType = "index.html", "text/html; charset=utf-8"
	case "/assets/app.js":
		file, contentType = "app.js", "text/javascript; charset=utf-8"
	case "/assets/style.css":
		file, contentType = "style.css", "text/css; charset=utf-8"
	case "/assets/app.js.LEGAL.txt":
		file, contentType = "app.js.LEGAL.txt", "text/plain; charset=utf-8"
	default:
		return false
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return true
	}
	connect := "'self'"
	if identity, ok := h.identity.(*SupabaseIdentity); ok {
		connect += " " + identity.origin
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src "+connect+"; base-uri 'none'; form-action 'self'; frame-ancestors 'none'; object-src 'none'")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	data, err := hostedWeb.ReadFile("hostedweb/" + file)
	if err != nil {
		http.NotFound(w, r)
		return true
	}
	w.Header().Set("Content-Type", contentType)
	if strings.HasPrefix(r.URL.Path, "/assets/") {
		w.Header().Set("Cache-Control", "no-cache")
	}
	if r.Method == "GET" {
		_, _ = w.Write(data)
	}
	return true
}
