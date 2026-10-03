package connector

import (
	"encoding/json"
	"strings"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"teamsbridge.local/teamsbridge/internal/graph"
)

func replyPortal() *bridgev2.Portal {
	return &bridgev2.Portal{Portal: &database.Portal{PortalKey: networkid.PortalKey{ID: "chat", Receiver: "login"}}}
}
func TestIncomingChatReply(t *testing.T) {
	var m graph.Message
	err := json.Unmarshal([]byte(`{"id":"2","body":{"contentType":"html","content":"<attachment id=\"1\"></attachment><p>Answer</p>"},"attachments":[{"id":"1","contentType":"messageReference","content":"{\"messageId\":\"1\",\"messagePreview\":\"Question\",\"messageSender\":{\"user\":{\"id\":\"self\"}}}"}]}`), &m)
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{meta: &Metadata{UserID: "self"}}
	got := c.convertChatMessage(replyPortal(), m)
	if got.ReplyTo == nil || got.ReplyTo.MessageID != "chat/1" || got.ReplyToLogin != "login" || got.ReplyToRoom.ID != "chat" {
		t.Fatalf("missing relation: %+v", got)
	}
	body := got.Parts[0].Content.Body
	if !strings.Contains(body, "Question") || !strings.Contains(body, "Answer") || strings.Contains(body, "Attachment:") {
		t.Fatalf("incorrect reply body %q", body)
	}
	m.Attachments[0].Content = `{"messageId":"2"}`
	if c.convertChatMessage(replyPortal(), m).ReplyTo != nil {
		t.Fatal("self reply accepted")
	}
}
func TestOutgoingChatReply(t *testing.T) {
	msg := &bridgev2.MatrixMessage{MatrixEventBase: bridgev2.MatrixEventBase[*event.MessageEventContent]{Portal: replyPortal(), Content: &event.MessageEventContent{MsgType: event.MsgText, Body: "Answer"}}}
	path, _, err := outgoingChatMessage(msg)
	if err != nil || path != "/chats/chat/messages" {
		t.Fatal(path, err)
	}
	msg.ReplyTo = &database.Message{ID: "chat/1"}
	path, payload, err := outgoingChatMessage(msg)
	if err != nil || path != "/chats/chat/messages/replyWithQuote" {
		t.Fatal(path, err)
	}
	encoded, _ := json.Marshal(payload)
	if string(encoded) != `{"messageIds":["1"],"replyMessage":{"body":{"content":"Answer","contentType":"text"}}}` {
		t.Fatal(string(encoded))
	}
	msg.ReplyTo.ID = "other/1"
	if _, _, err = outgoingChatMessage(msg); err == nil {
		t.Fatal("cross-chat reply accepted")
	}
	msg.ReplyTo = nil
	msg.ThreadRoot = &database.Message{ID: "chat/1"}
	if path, _, err = outgoingChatMessage(msg); err != nil || !strings.HasSuffix(path, "replyWithQuote") {
		t.Fatal(path, err)
	}
}
