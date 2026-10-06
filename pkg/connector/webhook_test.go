package connector

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"teamsbridge.local/teamsbridge/internal/graph"
)

func TestNotificationResourceBoundaries(t *testing.T) {
	tests := map[string]notificationTarget{
		"chats('19:chat@thread.v2')/messages('123')":                          {Portal: "19:chat@thread.v2", Message: "123", Path: "/chats/19:chat@thread.v2/messages/123"},
		"/chats/19%3Achat%40thread.v2/messages/123":                           {Portal: "19:chat@thread.v2", Message: "123", Path: "/chats/19:chat@thread.v2/messages/123"},
		"teams('team')/channels('19:channel')/messages('123')/replies('456')": {Portal: "channel:team:19:channel", Message: "456", ReplyTo: "123", Path: "/teams/team/channels/19:channel/messages/123/replies/456"},
	}
	for input, want := range tests {
		got, err := parseNotificationTarget(input)
		if err != nil || got != want {
			t.Fatalf("%s: %#v %v", input, got, err)
		}
	}
	for _, input := range []string{"https://evil.test/chats/x/messages/1", "/users/me/messages/1", "/chats/../messages/1", "/chats/x/messages/%2Ffoo", "/chats/x/messages/1?x=y", "/chats/x/messages/1/extra", "/teams/t/channels/c/messages/1/members/2"} {
		if _, err := parseNotificationTarget(input); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
}
func webhookTestClient(t *testing.T) *Client {
	t.Helper()
	raw, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)
	t.Cleanup(func() { raw.Close() })
	db, err := dbutil.NewWithDB(raw, "sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	main := &Connector{Config: Config{WebhookURL: "https://bridge.example.test", WebhookListen: "127.0.0.1:0"}}
	bridge := &bridgev2.Bridge{ID: "test"}
	bridge.DB = database.New(bridge.ID, main.GetDBMetaTypes(), db)
	main.Bridge = bridge
	// Minimal login table for exercising real persisted subscription updates.
	_, err = db.Exec(context.Background(), `CREATE TABLE user_login (bridge_id TEXT,id TEXT,user_mxid TEXT,remote_name TEXT,remote_profile TEXT,space_room TEXT,metadata TEXT)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(context.Background(), `INSERT INTO user_login (bridge_id,id,user_mxid) VALUES ('test','tenant:self','@self:test')`)
	if err != nil {
		t.Fatal(err)
	}
	meta := &Metadata{UserID: "self", WebhookSecret: "test-secret", Subscriptions: map[string]GraphSubscription{"/users/self/chats/getAllMessages": {ID: "sub", Resource: "/users/self/chats/getAllMessages", URL: main.Config.WebhookURL + webhookPath, Expires: time.Now().Add(2 * time.Hour)}}}
	c := &Client{main: main, meta: meta, webWake: make(chan struct{}, 1), login: &bridgev2.UserLogin{Bridge: bridge, UserLogin: &database.UserLogin{ID: "tenant:self", UserMXID: "@self:test", Metadata: meta}}}
	if err = main.startWebhook(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(main.Stop)
	main.webClients.Store(c.login.ID, c)
	return c
}
func TestWebhookValidationAuthenticationAndDurableQueue(t *testing.T) {
	c := webhookTestClient(t)
	req := httptest.NewRequest("POST", webhookPath+"?validationToken=hello%2Bworld%26x", nil)
	w := httptest.NewRecorder()
	c.main.handleWebhook(w, req)
	if w.Code != 200 || w.Body.String() != "hello+world&x" || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
		t.Fatal(w)
	}
	n := graphNotification{SubscriptionID: "sub", TenantID: "tenant", ClientState: "test-secret", Resource: "chats('chat')/messages('123')", Change: "updated"}
	send := func(n graphNotification) int {
		raw, _ := json.Marshal(map[string]any{"value": []graphNotification{n}})
		w := httptest.NewRecorder()
		c.main.handleWebhook(w, httptest.NewRequest("POST", webhookPath, strings.NewReader(string(raw))))
		return w.Code
	}
	for _, field := range []string{"secret", "tenant", "sub", "resource"} {
		bad := n
		switch field {
		case "secret":
			bad.ClientState = "wrong"
		case "tenant":
			bad.TenantID = "other"
		case "sub":
			bad.SubscriptionID = "other"
		case "resource":
			bad.Resource = "https://evil.test"
		}
		send(bad)
	}
	var count int
	db := c.main.Bridge.DB
	if err := db.QueryRow(context.Background(), "SELECT count(*) FROM teams_webhook_queue").Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid notification stored: %d %v", count, err)
	}
	if code := send(n); code != 202 {
		t.Fatal(code)
	}
	var stored string
	if err := db.QueryRow(context.Background(), "SELECT payload FROM teams_webhook_queue").Scan(&stored); err != nil || !strings.Contains(stored, "123") {
		t.Fatalf("acknowledged before persistence: %s %v", stored, err)
	}
	// A worker failure retains the durable notification for retry.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(429) }))
	defer server.Close()
	c.api = &graph.Client{Base: server.URL + "/v1.0", HTTP: server.Client(), Token: func(context.Context) (string, error) { return "test", nil }}
	if worked, err := c.processQueuedNotification(context.Background()); !worked || err != nil {
		t.Fatal(worked, err)
	}
	var retry int64
	if err := db.QueryRow(context.Background(), "SELECT retry_at FROM teams_webhook_queue").Scan(&retry); err != nil || retry <= time.Now().UnixMilli() {
		t.Fatal(retry, err)
	}
	// Database failure must ask Graph to redeliver, never falsely acknowledge.
	_, _ = db.Exec(context.Background(), "DROP TABLE teams_webhook_queue")
	if code := send(n); code != 503 {
		t.Fatal(code)
	}
}
func TestChannelNotificationScope(t *testing.T) {
	c := webhookTestClient(t)
	c.meta.Subscriptions = map[string]GraphSubscription{"channel": {ID: "channel-sub", Resource: "/teams/team/channels/channel/messages"}}
	n := graphNotification{SubscriptionID: "channel-sub", TenantID: "tenant", ClientState: "test-secret", Change: "created", Resource: "teams('team')/channels('channel')/messages('1')/replies('2')"}
	if !c.acceptNotification(n) {
		t.Fatal("valid reply rejected")
	}
	n.Resource = "teams('other')/channels('channel')/messages('1')"
	if c.acceptNotification(n) {
		t.Fatal("wrong channel accepted")
	}
	n.Lifecycle = "reauthorizationRequired"
	if !c.acceptNotification(n) {
		t.Fatal("lifecycle rejected")
	}
}
func TestSubscriptionCreateRenewAndRecover(t *testing.T) {
	c := webhookTestClient(t)
	c.meta.Subscriptions = map[string]GraphSubscription{}
	resource := "/users/self/chats/getAllMessages"
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method+" "+r.URL.Path)
		if !strings.HasPrefix(r.URL.Path, "/beta/subscriptions") {
			t.Error(r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			_, _ = w.Write([]byte(`{"value":[]}`))
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.Method == "POST" && (body["includeResourceData"] != false || body["lifecycleNotificationUrl"] != c.main.Config.WebhookURL+webhookPath || body["clientState"] != "test-secret") {
			t.Error(body)
		}
		_ = json.NewEncoder(w).Encode(GraphSubscription{ID: "new-sub", Expires: time.Now().Add(2 * time.Hour)})
	}))
	defer server.Close()
	c.api = &graph.Client{Base: server.URL + "/v1.0", HTTP: server.Client(), Token: func(context.Context) (string, error) { return "test", nil }}
	if err := c.ensureSubscription(context.Background(), resource); err != nil {
		t.Fatal(err)
	}
	s := c.meta.Subscriptions[resource]
	if s.ID != "new-sub" {
		t.Fatal(s)
	}
	var persisted string
	if err := c.main.Bridge.DB.QueryRow(context.Background(), "SELECT metadata FROM user_login").Scan(&persisted); err != nil || !strings.Contains(persisted, "new-sub") {
		t.Fatal(persisted, err)
	}
	if err := c.ensureSubscription(context.Background(), resource); err != nil {
		t.Fatal(err)
	}
	if len(methods) != 2 {
		t.Fatal("unnecessary subscription request", methods)
	}
	s.Expires = time.Now().Add(time.Hour)
	c.meta.Subscriptions[resource] = s
	if err := c.ensureSubscription(context.Background(), resource); err != nil {
		t.Fatal(err)
	}
	if methods[len(methods)-1] != "PATCH /beta/subscriptions/new-sub" {
		t.Fatal(methods)
	}
}

func TestPushDeletionAndReplyFetch(t *testing.T) {
	for _, status := range []int{200, 403, 404, 429} {
		c, _ := mockActions(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			if status == 200 {
				_, _ = w.Write([]byte(`{"id":"reply","messageType":"message"}`))
			}
		})
		target := notificationTarget{Message: "reply", ReplyTo: "root", Path: "/teams/t/channels/c/messages/root/replies/reply"}
		for _, change := range []string{"updated", "deleted"} {
			m, err := c.fetchNotificationMessage(context.Background(), target, change)
			if status == 200 {
				if err != nil || m.ID != "reply" || m.ReplyToID != "root" || m.Deleted != nil {
					t.Fatal(m, err)
				}
			} else if status == 404 && change == "deleted" {
				if err != nil || m.Deleted == nil || m.ID != "reply" {
					t.Fatal(m, err)
				}
			} else if err == nil {
				t.Fatalf("%s %d incorrectly treated as deletion", change, status)
			}
		}
	}
}
func TestSubscriptionRecoveryAndLifecycle(t *testing.T) {
	c := webhookTestClient(t)
	resource := "/users/self/chats/getAllMessages"
	s := c.meta.Subscriptions[resource]
	s.Expires = time.Now()
	c.meta.Subscriptions[resource] = s
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch r.Method {
		case "PATCH":
			w.WriteHeader(404)
		case "GET":
			_ = json.NewEncoder(w).Encode(map[string]any{"value": []GraphSubscription{{ID: "recovered", Resource: resource, URL: c.main.Config.WebhookURL + webhookPath, ClientState: "test-secret", Expires: time.Now().Add(time.Hour)}}})
		case "POST":
			if !strings.HasSuffix(r.URL.Path, "/recovered/reauthorize") {
				t.Error("unnecessary creation", r.URL.Path)
			}
			w.WriteHeader(204)
		}
	}))
	defer server.Close()
	c.api = &graph.Client{Base: server.URL + "/v1.0", HTTP: server.Client(), Token: func(context.Context) (string, error) { return "test", nil }}
	if err := c.ensureSubscription(context.Background(), resource); err != nil {
		t.Fatal(err)
	}
	if c.meta.Subscriptions[resource].ID != "recovered" {
		t.Fatal(c.meta.Subscriptions)
	}
	if err := c.processLifecycle(context.Background(), graphNotification{SubscriptionID: "recovered", Lifecycle: "reauthorizationRequired"}); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 3 {
		t.Fatal(paths)
	}
}
func TestWebhookRejectsMalformedBodies(t *testing.T) {
	c := &Connector{}
	for _, body := range []string{`{"value":[]} trailing`, `{"value":[]}`, strings.Repeat("x", (1<<20)+1), `{"value": [{"subscriptionId":"x"}]} {}`} {
		w := httptest.NewRecorder()
		c.handleWebhook(w, httptest.NewRequest("POST", webhookPath, strings.NewReader(body)))
		if w.Code != 400 {
			t.Fatal(w.Code)
		}
	}
	w := httptest.NewRecorder()
	c.handleWebhook(w, httptest.NewRequest("GET", webhookPath, nil))
	if w.Code != 405 {
		t.Fatal(w.Code)
	}
}
