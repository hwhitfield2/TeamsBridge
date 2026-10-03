package graph

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestRefreshRetainsRefreshTokenAndUsesTenant(t *testing.T) {
	o := OAuth{ClientID: "fb08a0e6-768a-4121-a1e9-6109dd0daa82", TenantID: "c5cc116b-0d7c-4694-a045-3144a35743b0"}
	o.HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if !strings.Contains(r.URL.Path, o.TenantID) {
			t.Fatal("wrong tenant")
		}
		r.ParseForm()
		if r.Form.Get("refresh_token") != "old" || r.Form.Get("client_secret") != "" || r.Form.Get("scope") != "" {
			t.Fatal("wrong refresh grant")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"access_token":"new","expires_in":3600}`)), Header: http.Header{}}, nil
	})}
	token, err := o.Refresh(context.Background(), "old")
	if err != nil || token.Refresh != "old" || token.Access != "new" || time.Until(token.ExpiresAt) < 59*time.Minute {
		t.Fatalf("bad refresh: %v", err)
	}
}
func TestCanceledDeviceCodePoll(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (OAuth{}).Poll(ctx, &Challenge{ExpiresIn: 900})
	if err != context.Canceled {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestSecretFileAndSafeDiagnostics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte("private-test-value"), 0600); err != nil {
		t.Fatal(err)
	}
	o := OAuth{ClientID: "fb08a0e6-768a-4121-a1e9-6109dd0daa82", TenantID: "c5cc116b-0d7c-4694-a045-3144a35743b0", SecretFile: path}
	o.HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		r.ParseForm()
		if r.Form.Get("client_secret") != "private-test-value" {
			t.Fatal("secret missing from token request")
		}
		return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader(`{"error":"invalid_client","error_codes":[7000218],"error_description":"private-test-value"}`)), Header: http.Header{}}, nil
	})}
	_, err := o.Refresh(context.Background(), "refresh")
	if err == nil || !strings.Contains(err.Error(), "AADSTS7000218") || strings.Contains(err.Error(), "private-test-value") {
		t.Fatalf("unsafe or missing diagnostic: %v", err)
	}
}
