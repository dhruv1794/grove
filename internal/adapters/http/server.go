// Package http is the web adapter: it exposes the grove core API over a
// localhost HTTP server (JSON + SSE) and serves the embedded React SPA. Like
// every adapter it holds no business logic — handlers translate requests into
// calls on a *grove.Grove and render the results. It binds 127.0.0.1 only.
package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"runtime/debug"
	"time"

	"grove/internal/adapters/http/web"
	"grove/internal/core"
	"grove/internal/grove"
)

// Options configures the web server.
type Options struct {
	Port      int    // listen port; 0 picks a free one
	Auth      string // if non-empty, require this bearer token / ?token= on /api/*
	OpenBrowser bool // open the default browser at the served URL
}

// Server serves the grove web UI and JSON/SSE API for one open Grove handle.
// It does not own the handle's lifecycle — the caller opens and closes it.
type Server struct {
	g    *grove.Grove
	opts Options
}

// New builds a web server over an open Grove handle.
func New(g *grove.Grove, opts Options) *Server {
	return &Server{g: g, opts: opts}
}

// handler builds the routed handler: /api/* JSON+SSE endpoints (auth-gated when
// a token is set) plus the embedded SPA with client-side-routing fallback.
func (s *Server) handler() (http.Handler, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/sources", s.handleSources)
	mux.HandleFunc("GET /api/trees", s.handleTrees)
	mux.HandleFunc("GET /api/graph", s.handleGraph)
	mux.HandleFunc("GET /api/tree/{id}", s.handleTree)
	mux.HandleFunc("GET /api/doc/{id}", s.handleDoc)
	mux.HandleFunc("POST /api/ask", s.handleAsk)
	mux.HandleFunc("POST /api/build", s.handleBuild)
	mux.HandleFunc("POST /api/sync", s.handleSync)
	mux.HandleFunc("POST /api/embed", s.handleEmbed)
	mux.HandleFunc("POST /api/connect", s.handleConnect)

	assets, err := web.FS()
	if err != nil {
		return nil, fmt.Errorf("load embedded web assets: %w", err)
	}
	mux.Handle("GET /", spaHandler(assets))

	return recoverMW(s.withAuth(mux)), nil
}

// recoverMW turns a handler panic into a logged stack trace + a 500 JSON error,
// so one bad request can't take down the server (and we can see the cause).
func recoverMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("grove web: panic serving %s %s: %v\n%s", r.Method, r.URL.Path, rec, debug.Stack())
				// Best-effort: only works if the handler hadn't already written
				// the header (e.g. an SSE stream mid-flight can't be reset).
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				json.NewEncoder(w).Encode(errBody{Code: string(core.KindGeneric), Message: fmt.Sprintf("internal error: %v", rec)})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// Serve runs the server until ctx is cancelled, then shuts down gracefully.
func (s *Server) Serve(ctx context.Context) error {
	h, err := s.handler()
	if err != nil {
		return err
	}
	// Localhost-only bind: the v0.1 trust model is "everything the local user can
	// read" — never expose the API on a routable interface.
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", s.opts.Port))
	if err != nil {
		return fmt.Errorf("listen on 127.0.0.1:%d: %w", s.opts.Port, err)
	}
	url := fmt.Sprintf("http://%s", ln.Addr().String())
	srv := &http.Server{Handler: h}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	fmt.Printf("grove web UI on %s  (Ctrl-C to stop)\n", url)
	if s.opts.OpenBrowser {
		_ = openBrowser(url)
	}

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// withAuth gates /api/* behind a bearer token when one is configured. The SPA
// and other GETs stay open (it's localhost). The token may arrive as
// `Authorization: Bearer <t>` or a `?token=<t>` query param (for SSE/EventSource,
// which can't set headers).
func (s *Server) withAuth(next http.Handler) http.Handler {
	if s.opts.Auth == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Path) >= 4 && r.URL.Path[:4] == "/api" {
			tok := r.URL.Query().Get("token")
			if h := r.Header.Get("Authorization"); tok == "" && len(h) > 7 && h[:7] == "Bearer " {
				tok = h[7:]
			}
			if tok != s.opts.Auth {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// spaHandler serves static assets, falling back to index.html for any path that
// isn't a real file — so client-side routes (e.g. /tree/x) load the app.
func spaHandler(assets fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(assets))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p != "/" {
			if f, err := assets.Open(p[1:]); err == nil {
				f.Close()
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		fileServer.ServeHTTP(w, r2)
	})
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return fmt.Errorf("no browser opener for %s", runtime.GOOS)
	}
	return cmd.Start()
}
