package graph

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const RedirectURI = "http://localhost:29319/oauth/callback"

type browserResult struct {
	code string
	err  error
}
type BrowserLogin struct {
	URL             string
	verifier, state string
	result          chan browserResult
	ctx             context.Context
	cancel          context.CancelFunc
	server          *http.Server
	once            sync.Once
}

func randomString() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func (o OAuth) StartBrowser() (*BrowserLogin, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	// Bind only loopback. The OAuth redirect keeps the exact registered localhost URI.
	listener, err := net.Listen("tcp4", "127.0.0.1:29319")
	if err != nil {
		return nil, fmt.Errorf("cannot listen for Microsoft sign-in on localhost:29319: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	b := &BrowserLogin{verifier: randomString(), state: randomString(), result: make(chan browserResult, 1), ctx: ctx, cancel: cancel}
	digest := sha256.Sum256([]byte(b.verifier))
	q := url.Values{"client_id": {o.ClientID}, "response_type": {"code"}, "redirect_uri": {RedirectURI}, "response_mode": {"query"}, "scope": {o.Scopes()}, "state": {b.state}, "code_challenge": {base64.RawURLEncoding.EncodeToString(digest[:])}, "code_challenge_method": {"S256"}}
	b.URL = "https://login.microsoftonline.com/" + o.TenantID + "/oauth2/v2.0/authorize?" + q.Encode()
	b.server = &http.Server{Handler: http.HandlerFunc(b.callback), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 10 * time.Second}
	go func() {
		if err := b.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			b.once.Do(func() { b.result <- browserResult{err: fmt.Errorf("OAuth callback listener failed")} })
		}
	}()
	go func() { <-ctx.Done(); _ = b.server.Close() }()
	return b, nil
}
func (b *BrowserLogin) Close() { b.cancel(); _ = b.server.Close() }
func (b *BrowserLogin) callback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if r.Host != "localhost:29319" || r.URL.Path != "/oauth/callback" {
		http.NotFound(w, r)
		return
	}
	if r.Method != "GET" {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q["state"]) != 1 || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(b.state)) != 1 {
		http.Error(w, "Invalid sign-in state. Return to Beeper and start a new login.", 400)
		return
	}
	if b.ctx.Err() != nil {
		http.Error(w, "This sign-in has expired. Start a new login in Beeper.", 410)
		return
	}
	var result browserResult
	if q.Get("error") != "" {
		result.err = fmt.Errorf("Microsoft declined browser sign-in; inspect the Microsoft error details or Entra sign-in logs")
	} else if len(q["code"]) != 1 || q.Get("code") == "" {
		http.Error(w, "Missing authorization code.", 400)
		return
	} else {
		result.code = q.Get("code")
	}
	accepted := false
	b.once.Do(func() { accepted = true; b.result <- result })
	if !accepted {
		http.Error(w, "This sign-in response was already received.", 409)
		return
	}
	fmt.Fprintln(w, "Microsoft sign-in response received. Return to Beeper to check whether sign-in completed.")
}
func (o OAuth) WaitBrowser(ctx context.Context, b *BrowserLogin) (*Token, error) {
	defer b.Close()
	var result browserResult
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-b.ctx.Done():
		return nil, b.ctx.Err()
	case result = <-b.result:
	}
	if result.err != nil {
		return nil, result.err
	}
	var t Token
	err := o.post(ctx, "token", url.Values{"grant_type": {"authorization_code"}, "code": {result.code}, "redirect_uri": {RedirectURI}, "code_verifier": {b.verifier}}, &t)
	if err != nil {
		return nil, err
	}
	if t.Access == "" || t.Refresh == "" {
		return nil, fmt.Errorf("Microsoft did not return access and refresh tokens")
	}
	t.ExpiresAt = time.Now().Add(time.Duration(t.ExpiresIn) * time.Second)
	return &t, nil
}
