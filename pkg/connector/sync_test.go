package connector

import (
	"context"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"teamsbridge.local/teamsbridge/internal/graph"
	"testing"
)

func TestChatIdentityAndMembers(t *testing.T) {
	c := &Client{meta: &Metadata{UserID: "self"}}
	chat := graph.Chat{ChatType: "oneOnOne", Members: []graph.Member{{UserID: "other", DisplayName: "Colleague"}}}
	info := c.chatInfo(chat)
	if *info.Name != "Colleague" || len(info.Members.MemberMap) != 1 {
		t.Fatalf("incorrect chat info: %+v", info)
	}
	if messageID("chat-a", "1") == messageID("chat-b", "1") {
		t.Fatal("message IDs collide across chats")
	}
}
func TestHTMLAndUnsupportedContent(t *testing.T) {
	var m graph.Message
	m.Body.ContentType = "html"
	m.Body.Content = "<p>Hello <b>world</b></p>"
	got := convert(m).Parts[0].Content.Body
	if got != "Hello **world**" {
		t.Fatalf("unexpected plain text %q", got)
	}
	m.Body.Content = ""
	if convert(m).Parts[0].Content.Body == "" {
		t.Fatal("empty message silently dropped")
	}
}

func TestLoginAvailableBeforeHistorySync(t *testing.T) {
	connector := &Connector{}
	login := &bridgev2.UserLogin{UserLogin: &database.UserLogin{Metadata: &Metadata{Token: graph.Token{Refresh: "saved-refresh"}}}}
	if err := connector.LoadUserLogin(context.Background(), login); err != nil {
		t.Fatal(err)
	}
	if !login.Client.IsLoggedIn() {
		t.Fatal("restored credentials must allow sends before history sync finishes")
	}
	empty := &bridgev2.UserLogin{UserLogin: &database.UserLogin{Metadata: &Metadata{}}}
	if err := connector.LoadUserLogin(context.Background(), empty); err != nil {
		t.Fatal(err)
	}
	if empty.Client.IsLoggedIn() {
		t.Fatal("missing credentials must not be considered logged in")
	}
}
