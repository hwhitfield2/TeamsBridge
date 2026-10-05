package connector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"teamsbridge.local/teamsbridge/internal/graph"
)

func mockActions(t *testing.T, handler http.HandlerFunc) (*Client, *bridgev2.Portal) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c := &Client{meta: &Metadata{UserID: "self", Token: graph.Token{Scope: "Chat.ReadWrite Chat.Create ChatMessage.Send ChannelMessage.Edit ChannelMessage.Send"}}, login: &bridgev2.UserLogin{UserLogin: &database.UserLogin{ID: "tenant:self"}}}
	c.api = &graph.Client{Base: server.URL + "/v1.0", HTTP: server.Client(), Token: func(context.Context) (string, error) { return "test", nil }}
	p := &bridgev2.Portal{Portal: &database.Portal{PortalKey: networkid.PortalKey{ID: "chat", Receiver: "tenant:self"}}}
	return c, p
}
func TestOutgoingMutationRoutes(t *testing.T) {
	var paths []string
	c, p := mockActions(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		if r.Method == "PATCH" {
			var payload struct{ Body struct{ Content string } }
			json.NewDecoder(r.Body).Decode(&payload)
			if payload.Body.Content != "changed" {
				t.Error("edit body lost")
			}
		}
		w.WriteHeader(204)
	})
	target := &database.Message{ID: "chat/123", SenderID: "self"}
	err := c.HandleMatrixEdit(context.Background(), &bridgev2.MatrixEdit{MatrixEventBase: bridgev2.MatrixEventBase[*event.MessageEventContent]{Portal: p, Content: &event.MessageEventContent{MsgType: event.MsgText, Body: "changed"}}, EditTarget: target})
	if err != nil {
		t.Fatal(err)
	}
	err = c.HandleMatrixMessageRemove(context.Background(), &bridgev2.MatrixMessageRemove{MatrixEventBase: bridgev2.MatrixEventBase[*event.RedactionEventContent]{Portal: p}, TargetMessage: target})
	if err != nil {
		t.Fatal(err)
	}
	reaction := &bridgev2.MatrixReaction{MatrixEventBase: bridgev2.MatrixEventBase[*event.ReactionEventContent]{Portal: p, Content: &event.ReactionEventContent{RelatesTo: event.RelatesTo{Key: "👍"}}}, TargetMessage: target}
	pre, err := c.PreHandleMatrixReaction(context.Background(), reaction)
	if err != nil {
		t.Fatal(err)
	}
	reaction.PreHandleResp = &pre
	if _, err = c.HandleMatrixReaction(context.Background(), reaction); err != nil {
		t.Fatal(err)
	}
	want := "PATCH /v1.0/chats/chat/messages/123|POST /v1.0/users/self/chats/chat/messages/123/softDelete|POST /v1.0/chats/chat/messages/123/setReaction"
	if strings.Join(paths, "|") != want {
		t.Fatal(paths)
	}
	target.SenderID = "other"
	if c.HandleMatrixMessageRemove(context.Background(), &bridgev2.MatrixMessageRemove{MatrixEventBase: bridgev2.MatrixEventBase[*event.RedactionEventContent]{Portal: p}, TargetMessage: target}) == nil {
		t.Fatal("deleted another user's message")
	}
}
func TestChannelEditScopeAndTarget(t *testing.T) {
	calls := 0
	c, p := mockActions(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if !strings.HasSuffix(r.URL.Path, "/messages/root/replies/reply") {
			t.Error(r.URL.Path)
		}
		w.WriteHeader(204)
	})
	p.ID = "channel:team:channel"
	target := &database.Message{ID: messageID(string(p.ID), "reply"), ThreadRoot: messageID(string(p.ID), "root"), SenderID: "self"}
	edit := &bridgev2.MatrixEdit{MatrixEventBase: bridgev2.MatrixEventBase[*event.MessageEventContent]{Portal: p, Content: &event.MessageEventContent{MsgType: event.MsgText, Body: "changed"}}, EditTarget: target}
	if err := c.HandleMatrixEdit(context.Background(), edit); err != nil {
		t.Fatal(err)
	}
	if err := c.HandleMatrixMessageRemove(context.Background(), &bridgev2.MatrixMessageRemove{MatrixEventBase: bridgev2.MatrixEventBase[*event.RedactionEventContent]{Portal: p}, TargetMessage: target}); err == nil {
		t.Fatal("delete accepted edit-only scope")
	}
	target.ThreadRoot = "other/root"
	if err := c.HandleMatrixEdit(context.Background(), edit); err == nil {
		t.Fatal("cross-room target accepted")
	}
	if calls != 1 {
		t.Fatal(calls)
	}
}
func TestCreateChatMembersAndReadOnly(t *testing.T) {
	calls := 0
	c, _ := mockActions(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		var b struct {
			ChatType string
			Topic    string
			Members  []map[string]any
		}
		json.NewDecoder(r.Body).Decode(&b)
		if b.ChatType != "group" || b.Topic != "Work" || len(b.Members) != 3 {
			t.Errorf("bad group payload %+v", b)
		}
		w.Write([]byte(`{"id":"new","chatType":"group"}`))
	})
	got, err := c.createChat(context.Background(), "group", "Work", []networkid.UserID{"one", "two", "one", "self"})
	if err != nil || got.PortalKey.ID != "new" {
		t.Fatal(got, err)
	}
	c.meta.Auth = &LoginSettings{Profile: "readonly"}
	if _, err = c.createChat(context.Background(), "oneOnOne", "", []networkid.UserID{"one"}); err == nil || calls != 1 {
		t.Fatal("readonly created chat")
	}
}
func TestReadReceiptDoesNotMarkUnseenNewMessages(t *testing.T) {
	calls := 0
	now := time.Now().UTC()
	c, p := mockActions(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			calls++
			w.WriteHeader(204)
		} else {
			json.NewEncoder(w).Encode(map[string]any{"value": []map[string]any{{"createdDateTime": now}}})
		}
	})
	msg := &bridgev2.MatrixReadReceipt{Portal: p, ExactMessage: &database.Message{}, ReadUpTo: now.Add(-time.Minute)}
	if err := c.HandleMatrixReadReceipt(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("marked newer messages read")
	}
	msg.ReadUpTo = now
	if err := c.HandleMatrixReadReceipt(context.Background(), msg); err != nil || calls != 1 {
		t.Fatal(err, calls)
	}
}
