package mcpclient

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// openFunc opens a URL in the user's browser. Injectable so tests can substitute
// a fake that drives the callback instead of launching a real browser.
type openFunc func(url string) error

// defaultOpen launches the OS default browser.
func defaultOpen(rawURL string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", rawURL).Start()
	case "windows":
		return exec.Command("cmd", "/c", "start", rawURL).Start()
	default:
		return exec.Command("xdg-open", rawURL).Start()
	}
}

// browserAuthFetcher returns an AuthorizationCodeFetcher that performs the
// interactive leg of the OAuth authorization-code flow: it serves a one-shot
// localhost callback on redirectURL, opens the authorization URL in the browser,
// and waits for the redirect carrying ?code=&state=. The SDK handles everything
// else (discovery, DCR, PKCE, token exchange/refresh).
func browserAuthFetcher(redirectURL string, open openFunc) auth.AuthorizationCodeFetcher {
	return func(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
		u, err := url.Parse(redirectURL)
		if err != nil {
			return nil, fmt.Errorf("invalid redirect url %q: %w", redirectURL, err)
		}

		type result struct {
			code, state string
			err         error
		}
		ch := make(chan result, 1)

		mux := http.NewServeMux()
		mux.HandleFunc(u.Path, func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			if e := q.Get("error"); e != "" {
				http.Error(w, "authorization failed: "+e, http.StatusBadRequest)
				ch <- result{err: fmt.Errorf("authorization server returned error %q", e)}
				return
			}
			code := q.Get("code")
			if code == "" {
				http.Error(w, "missing authorization code", http.StatusBadRequest)
				ch <- result{err: fmt.Errorf("callback missing authorization code")}
				return
			}
			io.WriteString(w, "Authorization complete — you may close this tab and return to GoForge.")
			ch <- result{code: code, state: q.Get("state")}
		})

		ln, err := net.Listen("tcp", u.Host)
		if err != nil {
			return nil, fmt.Errorf("oauth callback: listen on %s: %w", u.Host, err)
		}
		srv := &http.Server{Handler: mux}
		go func() { _ = srv.Serve(ln) }()
		defer srv.Close()

		if err := open(args.URL); err != nil {
			// Fall back to manual: print the URL so a headless/no-browser user
			// can still complete the flow by pasting it.
			fmt.Fprintf(os.Stderr, "Open this URL to authorize the MCP server:\n%s\n", args.URL)
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case r := <-ch:
			if r.err != nil {
				return nil, r.err
			}
			return &auth.AuthorizationResult{Code: r.code, State: r.state}, nil
		}
	}
}

// newAuthCodeHandler builds an SDK authorization-code OAuthHandler. When client
// is non-nil it is used as a preregistered client; otherwise Dynamic Client
// Registration is configured with redirectURL.
func newAuthCodeHandler(redirectURL string, client *oauthex.ClientCredentials, open openFunc) (*auth.AuthorizationCodeHandler, error) {
	cfg := &auth.AuthorizationCodeHandlerConfig{
		RedirectURL:              redirectURL,
		AuthorizationCodeFetcher: browserAuthFetcher(redirectURL, open),
	}
	if client != nil {
		cfg.PreregisteredClient = client
	} else {
		cfg.DynamicClientRegistrationConfig = &auth.DynamicClientRegistrationConfig{
			Metadata: &oauthex.ClientRegistrationMetadata{RedirectURIs: []string{redirectURL}},
		}
	}
	return auth.NewAuthorizationCodeHandler(cfg)
}
