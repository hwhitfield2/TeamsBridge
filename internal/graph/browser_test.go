package graph

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func testBrowser(t *testing.T) *BrowserLogin {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &BrowserLogin{state: "expected", ctx: ctx, cancel: cancel, result: make(chan browserResult, 1)}
}
func TestCallbackRejectsInvalidRequestsAndReplay(t *testing.T) {
	b := testBrowser(t)
	for _, target := range []string{"http://localhost:29319/oauth/callback?state=wrong&code=secret", "http://localhost:29319/oauth/callback?state=expected&state=extra&code=secret", "http://localhost:29319/oauth/callback?state=expected", "http://evil.example/oauth/callback?state=expected&code=secret"} {
		w := httptest.NewRecorder()
		b.callback(w, httptest.NewRequest("GET", target, nil))
		if w.Code < 400 {
			t.Fatalf("accepted %s", target)
		}
		if len(b.result) != 0 {
			t.Fatal("invalid request consumed login")
		}
	}
	for i, want := range []int{200, 409} {
		w := httptest.NewRecorder()
		b.callback(w, httptest.NewRequest("GET", RedirectURI+"?state=expected&code=secret", nil))
		if w.Code != want {
			t.Fatalf("request %d: %d", i, w.Code)
		}
		if strings.Contains(w.Body.String(), "secret") {
			t.Fatal("leaked code")
		}
	}
	if result := <-b.result; result.code != "secret" {
		t.Fatal("lost authorization code")
	}
}
func TestBrowserPKCEAndCodeExchange(t *testing.T) {
	o := OAuth{ClientID: "fb08a0e6-768a-4121-a1e9-6109dd0daa82", TenantID: "c5cc116b-0d7c-4694-a045-3144a35743b0"}
	b, err := o.StartBrowser()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	parsed, _ := url.Parse(b.URL)
	q := parsed.Query()
	digest := sha256.Sum256([]byte(b.verifier))
	if q.Get("redirect_uri") != RedirectURI || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") != base64.RawURLEncoding.EncodeToString(digest[:]) {
		t.Fatal("invalid PKCE authorize URL")
	}
	o.HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		r.ParseForm()
		if r.Form.Get("code_verifier") != b.verifier || r.Form.Get("redirect_uri") != RedirectURI || r.Form.Get("code") != "test-code" || r.Form.Get("grant_type") != "authorization_code" {
			t.Fatal("incorrect token exchange")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"access_token":"access","refresh_token":"refresh","expires_in":3600}`)), Header: http.Header{}}, nil
	})}
	req, _ := http.NewRequest("GET", "http://127.0.0.1:29319/oauth/callback?state="+url.QueryEscape(b.state)+"&code=test-code", nil)
	req.Host = "localhost:29319"
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatal(res.Status)
	}
	token, err := o.WaitBrowser(context.Background(), b)
	if err != nil || token.Access != "access" {
		t.Fatalf("exchange failed: %v", err)
	}
}
func TestBrowserCancelClosesListener(t *testing.T) {
	o := OAuth{ClientID: "fb08a0e6-768a-4121-a1e9-6109dd0daa82", TenantID: "c5cc116b-0d7c-4694-a045-3144a35743b0"}
	b, err := o.StartBrowser()
	if err != nil {
		t.Fatal(err)
	}
	b.Close()
	if _, err = o.WaitBrowser(context.Background(), b); err != context.Canceled {
		t.Fatalf("expected canceled, got %v", err)
	}
}
