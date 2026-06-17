package mcpclient

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
)

func TestBrowserAuthFetcher_capturesCode(t *testing.T) {
	redirect := "http://localhost:18799/callback"

	// Fake browser: instead of opening a window, immediately hit the callback
	// with a code, echoing the state the authorization URL carried.
	fakeOpen := func(authURL string) error {
		u, err := url.Parse(authURL)
		if err != nil {
			return err
		}
		state := u.Query().Get("state")
		go func() {
			_, _ = http.Get(redirect + "?code=test-code&state=" + state)
		}()
		return nil
	}

	fetcher := browserAuthFetcher(redirect, fakeOpen)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	res, err := fetcher(ctx, &auth.AuthorizationArgs{URL: "https://issuer/authorize?state=xyz123&client_id=c"})
	if err != nil {
		t.Fatalf("fetcher: %v", err)
	}
	if res.Code != "test-code" {
		t.Errorf("code = %q, want test-code", res.Code)
	}
	if res.State != "xyz123" {
		t.Errorf("state = %q, want xyz123 (echoed from auth URL)", res.State)
	}

	// Callback server must be torn down — the port is free again.
	ln, err := net.Listen("tcp", "localhost:18799")
	if err != nil {
		t.Errorf("callback server not shut down (port still bound): %v", err)
	} else {
		_ = ln.Close()
	}
}

func TestBrowserAuthFetcher_callbackError(t *testing.T) {
	redirect := "http://localhost:18801/callback"
	fakeOpen := func(string) error {
		go func() { _, _ = http.Get(redirect + "?error=access_denied") }()
		return nil
	}
	fetcher := browserAuthFetcher(redirect, fakeOpen)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := fetcher(ctx, &auth.AuthorizationArgs{URL: "https://issuer/authorize"}); err == nil {
		t.Error("expected error when callback carries ?error=")
	}
}

func TestBrowserAuthFetcher_ctxTimeout(t *testing.T) {
	// Browser that never completes the flow.
	fetcher := browserAuthFetcher("http://localhost:18803/callback", func(string) error { return nil })
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := fetcher(ctx, &auth.AuthorizationArgs{URL: "https://issuer/authorize"}); err == nil {
		t.Error("expected context timeout error")
	}
}

func TestNewAuthCodeHandler(t *testing.T) {
	h, err := newAuthCodeHandler("http://localhost:8765/callback", nil, func(string) error { return nil })
	if err != nil {
		t.Fatalf("DCR handler: %v", err)
	}
	if h == nil {
		t.Error("expected non-nil handler")
	}
	// compile-time: it is an OAuthHandler.
	var _ auth.OAuthHandler = h
}
