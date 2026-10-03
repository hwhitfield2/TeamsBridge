package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"strings"
	"teamsbridge.local/teamsbridge/internal/graph"
	"testing"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func TestOutgoingMentions(t *testing.T) {
	content := &event.MessageEventContent{Format: event.FormatHTML, FormattedBody: `Hi <a href="https://matrix.to/#/@alice:test">Alice &amp; Bob</a> &lt;safe&gt; <a href="https://matrix.to/#/@alice:test">Alice</a>`, Mentions: &event.Mentions{UserIDs: []id.UserID{"@alice:test"}}}
	body, refs, err := outgoingMentions(content, func(user id.UserID) (string, error) {
		if user != "@alice:test" {
			t.Fatal(user)
		}
		return "teams-user", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 || refs[0].Mentioned.User.ID != "teams-user" || refs[0].Text != "Alice & Bob" || !strings.Contains(body, `<at id="1">Alice</at>`) || !strings.Contains(body, "&lt;safe&gt;") {
		t.Fatalf("bad conversion %q %+v", body, refs)
	}
	content.Mentions = &event.Mentions{}
	_, refs, err = outgoingMentions(content, func(id.UserID) (string, error) { t.Fatal("non-mention link resolved"); return "", nil })
	if err != nil || len(refs) != 0 {
		t.Fatal(refs, err)
	}
	content.Mentions = &event.Mentions{UserIDs: []id.UserID{"@missing:test"}}
	body, refs, err = outgoingMentions(content, func(id.UserID) (string, error) { return "missing-remote", nil })
	if err != nil || len(refs) != 1 || refs[0].Mentioned.User.ID != "missing-remote" || !strings.Contains(body, `<at id="0">@missing:test</at>`) {
		t.Fatal(body, refs, err)
	}
}
func TestInboundMentionEscaping(t *testing.T) {
	content := &event.MessageEventContent{Body: "Hello TOKEN and <script>"}
	applyMentions(content, []inboundMention{{marker: "TOKEN", label: "Alice & Bob", user: "@alice:test"}})
	if content.Body != "Hello @Alice & Bob and <script>" || !content.Mentions.Has("@alice:test") || strings.Contains(content.FormattedBody, "<script>") || !strings.Contains(content.FormattedBody, "https://matrix.to/#/@alice:test") {
		t.Fatalf("bad mention %+v", content)
	}
}

func TestTeamsMentionRoundTripToBeeper(t *testing.T) {
	var m graph.Message
	if err := json.Unmarshal([]byte(`{"body":{"contentType":"html","content":"<p>Hello <at id=\"0\">Me</at> and <at id=\"1\">Everyone</at></p>"},"mentions":[{"id":0,"mentionText":"Me","mentioned":{"user":{"id":"self"}}},{"id":1,"mentionText":"Everyone","mentioned":{"conversation":{"id":"chat","conversationIdentityType":"chat"}}}]}`), &m); err != nil {
		t.Fatal(err)
	}
	c := &Client{meta: &Metadata{UserID: "self"}, login: &bridgev2.UserLogin{UserLogin: &database.UserLogin{UserMXID: "@me:test"}}}
	out := c.convertChatMessage(replyPortal(), m).Parts[0].Content
	if !out.Mentions.Has("@me:test") || !out.Mentions.Room || out.Body != "Hello @Me and @Everyone" {
		t.Fatalf("incorrect mentions %+v", out)
	}
}

func TestMentionInsideOutgoingReply(t *testing.T) {
	c := &Client{meta: &Metadata{UserID: "self"}, login: &bridgev2.UserLogin{UserLogin: &database.UserLogin{UserMXID: "@me:test"}}}
	msg := &bridgev2.MatrixMessage{MatrixEventBase: bridgev2.MatrixEventBase[*event.MessageEventContent]{Portal: replyPortal(), Content: &event.MessageEventContent{Body: "Hi Me", Format: event.FormatHTML, FormattedBody: `Hi <a href="https://matrix.to/#/@me:test">Me</a>`, Mentions: &event.Mentions{UserIDs: []id.UserID{"@me:test"}}}}, ReplyTo: &database.Message{ID: "chat/1"}}
	_, payload, err := outgoingChatMessage(msg)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.addOutgoingMentions(context.Background(), msg, payload); err != nil {
		t.Fatal(err)
	}
	message := payload.(map[string]any)
	if message["messageIds"].([]string)[0] != "1" || len(message["replyMessage"].(map[string]any)["mentions"].([]graph.Mention)) != 1 {
		t.Fatal("reply wrapper lost mentions")
	}
}

func TestMetadataOnlyMentionPreservesPlainText(t *testing.T) {
	content := &event.MessageEventContent{Body: "Hello Alice <check this>", Mentions: &event.Mentions{UserIDs: []id.UserID{"@alice:test", "@alice:test"}}}
	body, refs, err := outgoingMentions(content, func(user id.UserID) (string, error) { return "alice-teams-id", nil })
	if err != nil || len(refs) != 1 || refs[0].Mentioned.User.ID != "alice-teams-id" || !strings.HasPrefix(body, "Hello Alice &lt;check this&gt;") || !strings.Contains(body, `<at id="0">`) {
		t.Fatalf("metadata-only mention failed: %q %+v %v", body, refs, err)
	}
	_, _, err = outgoingMentions(content, func(id.UserID) (string, error) { return "", fmt.Errorf("unknown user") })
	if err == nil {
		t.Fatal("unknown user accepted")
	}
}
