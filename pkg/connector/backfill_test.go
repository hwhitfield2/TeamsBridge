package connector

import (
	"context"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"net/http"
	"net/http/httptest"
	"strings"
	"teamsbridge.local/teamsbridge/internal/graph"
	"testing"
	"time"
)

func TestBackfillPaginationAndOrdering(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("$orderby") != "createdDateTime desc" {
			t.Error("wrong ordering")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"@odata.nextLink":"https://graph.microsoft.com/v1.0/chats/chat/messages?$skiptoken=next","value":[{"id":"2","messageType":"message","createdDateTime":"2026-10-02T10:00:00Z","from":{"user":{"id":"other"}},"body":{"content":"newer"}},{"id":"1","messageType":"message","createdDateTime":"2026-10-01T10:00:00Z","from":{"user":{"id":"other"}},"body":{"content":"older"}},{"id":"system","messageType":"systemEventMessage"}]}`))
	}))
	defer server.Close()
	c := &Client{meta: &Metadata{UserID: "self"}, api: &graph.Client{Base: server.URL + "/v1.0", HTTP: server.Client(), Token: func(context.Context) (string, error) { return "test", nil }}}
	p := bridgev2.FetchMessagesParams{Portal: &bridgev2.Portal{Portal: &database.Portal{PortalKey: networkid.PortalKey{ID: "chat"}}}, Count: 50}
	got, err := c.FetchMessages(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 2 || got.Messages[0].ID != "chat/1" || !got.HasMore || !got.AggressiveDeduplication || !strings.Contains(string(got.Cursor), "skiptoken=next") {
		t.Fatalf("bad backfill result: %+v", got)
	}
	p.AnchorMessage = &database.Message{Timestamp: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
	empty, err := c.FetchMessages(context.Background(), p)
	if err != nil || len(empty.Messages) != 0 || !empty.HasMore || empty.Cursor != got.Cursor {
		t.Fatal("overlapping page must advance without emitting out-of-order messages", err)
	}

}
