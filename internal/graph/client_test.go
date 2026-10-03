package graph

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPaginationAndAuthorization(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing auth")
		}
		if r.URL.Path == "/v1.0/chats" {
			json.NewEncoder(w).Encode(map[string]any{"value": []User{{ID: "one"}}, "@odata.nextLink": server.URL + "/v1.0/next"})
		} else {
			json.NewEncoder(w).Encode(map[string]any{"value": []User{{ID: "two"}}})
		}
	}))
	defer server.Close()
	c := &Client{HTTP: server.Client(), Base: server.URL + "/v1.0", Token: func(context.Context) (string, error) { return "test-token", nil }}
	got, err := List[User](context.Background(), c, "/chats")
	if err != nil || len(got) != 2 || got[1].ID != "two" {
		t.Fatalf("pagination: %v %v", got, err)
	}
}
func TestRejectsForeignNextLinkBeforeToken(t *testing.T) {
	c := &Client{Token: func(context.Context) (string, error) { t.Fatal("token accessed for foreign host"); return "", nil }}
	if err := c.Do(context.Background(), "GET", "https://attacker.example/v1.0/chats", nil, nil); err == nil {
		t.Fatal("accepted foreign URL")
	}
}
func TestRateLimitNotAutomaticallyRetried(t *testing.T) {
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Retry-After", "90")
		w.WriteHeader(429)
		w.Write([]byte(`{"error":{"code":"TooManyRequests","message":"secret"}}`))
	}))
	defer s.Close()
	c := &Client{Base: s.URL + "/v1.0", HTTP: s.Client(), Token: func(context.Context) (string, error) { return "t", nil }}
	err := c.Do(context.Background(), "POST", "/chats", map[string]string{"text": "hello"}, nil)
	var e *APIError
	if !errors.As(err, &e) || e.RetryAfter != 90*time.Second || calls != 1 {
		t.Fatalf("bad throttling: %v calls=%d", err, calls)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatal("leaked server response")
	}
}
func TestOAuthConfigValidation(t *testing.T) {
	if (OAuth{ClientID: "x", TenantID: "../../common"}).Validate() == nil {
		t.Fatal("accepted invalid authority")
	}
	if (OAuth{ClientID: "fb08a0e6-768a-4121-a1e9-6109dd0daa82", TenantID: "c5cc116b-0d7c-4694-a045-3144a35743b0"}).Validate() != nil {
		t.Fatal("rejected UUID")
	}
}
func TestPaginationCycle(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"value":[],"@odata.nextLink":"/chats"}`))
	}))
	defer s.Close()
	c := &Client{Base: s.URL + "/v1.0", HTTP: s.Client(), Token: func(context.Context) (string, error) { return "t", nil }}
	if _, err := List[Chat](context.Background(), c, "/chats"); err == nil {
		t.Fatal("accepted cyclic pagination")
	}
}
