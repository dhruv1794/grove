package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"time"

	"golang.org/x/oauth2"
)

// Authorize runs a browser-based OAuth 2.0 authorization-code flow on a
// loopback redirect (the "installed app" pattern Google and Atlassian support).
// It picks a free localhost port, opens the user's browser to the consent
// screen, captures the redirected code on a one-shot local server, and
// exchanges it for a token. The returned token includes a refresh token when
// the provider grants offline access.
//
// cfg.RedirectURL is overwritten with the loopback address. Pass port=0 to
// pick a free port (works for Google Desktop-app clients, which accept any
// loopback callback); pass a fixed port when the provider requires the
// callback URL match an exact pre-registered value (Atlassian's case).
// extraAuthParams are appended to the authorization URL (e.g. Atlassian
// requires audience=api.atlassian.com).
func Authorize(ctx context.Context, cfg *oauth2.Config, port int, extraAuthParams ...oauth2.AuthCodeOption) (*oauth2.Token, error) {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("start loopback server on %s: %w", addr, err)
	}
	defer ln.Close()
	gotPort := ln.Addr().(*net.TCPAddr).Port
	cfg.RedirectURL = fmt.Sprintf("http://127.0.0.1:%d/callback", gotPort)

	state, err := randomState()
	if err != nil {
		return nil, err
	}

	// AccessTypeOffline + prompt=consent so we reliably get a refresh token,
	// even on re-authorization of an already-consented account. Extra params
	// come last so callers (e.g. Atlassian's audience) override or augment.
	opts := append([]oauth2.AuthCodeOption{
		oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("prompt", "consent"),
	}, extraAuthParams...)
	authURL := cfg.AuthCodeURL(state, opts...)

	type result struct {
		code string
		err  error
	}
	resCh := make(chan result, 1)
	srv := &http.Server{}
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if e := q.Get("error"); e != "" {
			fmt.Fprintf(w, "Authorization failed: %s. You can close this tab.", e)
			resCh <- result{err: fmt.Errorf("authorization denied: %s", e)}
			return
		}
		if q.Get("state") != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			resCh <- result{err: fmt.Errorf("oauth state mismatch (possible CSRF)")}
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			resCh <- result{err: fmt.Errorf("no authorization code in callback")}
			return
		}
		io.WriteString(w, "grove is now authorized. You can close this tab and return to the terminal.")
		resCh <- result{code: code}
	})
	srv.Handler = mux
	go srv.Serve(ln)
	defer srv.Close()

	if err := openBrowser(authURL); err != nil {
		// Not fatal: print the URL so the user can open it manually.
		fmt.Printf("Open this URL in your browser to authorize grove:\n\n%s\n\n", authURL)
	} else {
		fmt.Printf("Opened your browser to authorize grove. Waiting for approval…\n")
		fmt.Printf("If it didn't open, visit:\n\n%s\n\n", authURL)
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-resCh:
		if res.err != nil {
			return nil, res.err
		}
		exchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		tok, err := cfg.Exchange(exchCtx, res.code)
		if err != nil {
			return nil, fmt.Errorf("exchange authorization code: %w", err)
		}
		return tok, nil
	}
}

// persistSource wraps an oauth2.TokenSource, writing any refreshed token back to
// the store so the refresh survives the process.
type persistSource struct {
	src   oauth2.TokenSource
	store *Store
	name  string
	last  string
}

func (p *persistSource) Token() (*oauth2.Token, error) {
	tok, err := p.src.Token()
	if err != nil {
		return nil, err
	}
	if tok.AccessToken != p.last {
		if err := p.store.Save(p.name, tok); err != nil {
			return nil, fmt.Errorf("persist refreshed token: %w", err)
		}
		p.last = tok.AccessToken
	}
	return tok, nil
}

// HTTPClient returns an *http.Client that authenticates requests with the stored
// token for name, transparently refreshing it (and persisting the refreshed
// token back to the store) as it expires. Used by connectors at runtime.
func HTTPClient(ctx context.Context, cfg *oauth2.Config, store *Store, name string) (*http.Client, error) {
	tok, err := store.Load(name)
	if err != nil {
		return nil, err
	}
	base := cfg.TokenSource(ctx, tok)
	ps := &persistSource{src: base, store: store, name: name, last: tok.AccessToken}
	return oauth2.NewClient(ctx, oauth2.ReuseTokenSource(tok, ps)), nil
}

func randomState() (string, error) {
	b := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", fmt.Errorf("generate oauth state: %w", err)
	}
	return hex.EncodeToString(b), nil
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
