package connector

import (
	"context"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"net/http"
	"net/http/httptest"
	"strings"
	"teamsbridge.local/teamsbridge/internal/graph"
	"testing"
	"time"
)

func TestChannelPathsAndReplies(t *testing.T) {
	portal := "channel:team:19:leadership@thread.tacv2"
	path, ok := channelPath(portal)
	if !ok || !strings.HasPrefix(path, "/teams/team/channels/") {
		t.Fatal(path)
	}
	m := graph.Message{ID: "reply", ReplyToID: "root"}
	if messageResource(portal, m) != path+"/messages/root/replies/reply" {
		t.Fatal("wrong hosted-content reply path")
	}
	p := &bridgev2.Portal{Portal: &database.Portal{PortalKey: networkid.PortalKey{ID: networkid.PortalID(portal)}}}
	c := &Client{meta: &Metadata{}}
	out := c.convertChatMessage(p, m)
	if out.ThreadRoot == nil || *out.ThreadRoot != messageID(portal, "root") {
		t.Fatal("lost thread root")
	}
	msg := &bridgev2.MatrixMessage{MatrixEventBase: bridgev2.MatrixEventBase[*event.MessageEventContent]{Portal: p, Content: &event.MessageEventContent{MsgType: event.MsgText, Body: "answer"}}, ReplyTo: &database.Message{ID: messageID(portal, "reply"), ThreadRoot: messageID(portal, "root")}}
	got, _, err := outgoingChatMessage(msg)
	if err != nil || got != path+"/messages/root/replies" {
		t.Fatal(got, err)
	}
}
func TestChannelReplyPagination(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"value":[{"id":"reply2","createdDateTime":"2026-10-02T12:00:00Z"}]}`))
	}))
	defer server.Close()
	c := &Client{api: &graph.Client{Base: server.URL + "/v1.0", HTTP: server.Client(), Token: func(context.Context) (string, error) { return "test", nil }}}
	roots := []graph.Message{{ID: "root", Created: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), Replies: []graph.Message{{ID: "reply1", Created: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)}}, RepliesNext: server.URL + "/v1.0/replies"}}
	got, err := c.flattenChannel(context.Background(), roots)
	if err != nil || len(got) != 3 || got[0].ID != "root" || got[2].ReplyToID != "root" {
		t.Fatal(got, err)
	}
}
