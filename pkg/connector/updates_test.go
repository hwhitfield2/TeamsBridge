package connector

import (
	"context"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"strings"
	"teamsbridge.local/teamsbridge/internal/graph"
	"testing"
	"time"
)

func TestIncomingEditsAndReactionIndependence(t *testing.T) {
	c := &Client{meta: &Metadata{UserID: "self"}}
	p := &bridgev2.Portal{Portal: &database.Portal{PortalKey: networkid.PortalKey{ID: "chat"}}}
	m := graph.Message{ID: "1"}
	m.From.User = &graph.User{ID: "other"}
	m.Body.Content = "original"
	oldHash := contentHash(m)
	part := &database.Message{ID: "chat/1", Metadata: &MessageMetadata{ContentHash: oldHash}}
	m.Modified = time.Now()
	m.Reactions = []graph.Reaction{{Type: "like"}}
	if contentHash(m) != oldHash {
		t.Fatal("reaction changed content hash")
	}
	result, err := c.handleExisting(context.Background(), p, nil, []*database.Message{part}, m)
	if err != nil || len(result.SubEvents) != 0 {
		t.Fatal("unchanged message edited", err)
	}
	m.Body.Content = "edited"
	m.Edited = &m.Modified
	result, err = c.handleExisting(context.Background(), p, nil, []*database.Message{part}, m)
	if err != nil || len(result.SubEvents) != 1 {
		t.Fatal(result, err)
	}
	edit, err := c.convertEdit(context.Background(), p, nil, []*database.Message{part}, m)
	if err != nil || len(edit.ModifiedParts) != 1 || edit.ModifiedParts[0].Content.Body != "edited" {
		t.Fatal(edit, err)
	}
	if part.Metadata.(*MessageMetadata).ContentHash != contentHash(m) {
		t.Fatal("edit metadata not updated")
	}
}
func TestReactionsAndBotCards(t *testing.T) {
	c := &Client{meta: &Metadata{UserID: "self"}}
	m := graph.Message{Reactions: []graph.Reaction{{Type: "heart"}}}
	m.Reactions[0].User.User = &graph.User{ID: "other"}
	got := c.reactionData(m)
	if !got.HasAllUsers || got.Users["other"].Reactions[0].Emoji != "❤️" {
		t.Fatal(got)
	}
	m.From.Application = &graph.User{ID: "bot", DisplayName: "Builds"}
	m = c.normalizeSender(m)
	if m.From.User.ID != "app-bot" {
		t.Fatal(m.From)
	}
	card := renderCard(`{"type":"AdaptiveCard","body":[{"type":"TextBlock","text":"Build passed"},{"type":"FactSet","facts":[{"title":"Status","value":"Ready"}]}],"actions":[{"type":"Action.OpenUrl","title":"Review","url":"https://example.com"},{"type":"Action.Submit","title":"Approve"}]}`)
	if !strings.Contains(card, "Build passed") || !strings.Contains(card, "Status") || !strings.Contains(card, "https://example.com") || !strings.Contains(card, "open Teams") {
		t.Fatal(card)
	}
	if strings.Contains(renderCard(`{"type":"Action.OpenUrl","url":"javascript:bad()"}`), "javascript:") {
		t.Fatal("unsafe card link")
	}
	for _, kind := range []string{"oneOnOne", "group", "meeting"} {
		if !supportedChatType(kind) {
			t.Fatal(kind)
		}
	}
	if supportedChatType("unknown") {
		t.Fatal("unknown type")
	}
}
func TestFileURLBoundaries(t *testing.T) {
	for _, url := range []string{"http://tenant.sharepoint.com/file", "https://localhost/file", "https://tenant.sharepoint.com.evil.com/file", "https://user@tenant.sharepoint.com/file", "https://tenant.sharepoint.com:444/file"} {
		if safeDownloadURL(url) {
			t.Fatal(url)
		}
	}
	if !safeDownloadURL("https://tenant.sharepoint.com/file") {
		t.Fatal("valid host denied")
	}
	_, err := dbMessageResource("chat", &database.Message{ID: networkid.MessageID("elsewhere/id")})
	if err == nil {
		t.Fatal("cross-room mutation allowed")
	}
}

func TestPartiallyDeliveredEditIsRetried(t *testing.T) {
	parts := []*database.Message{{Metadata: &MessageMetadata{ContentHash: "new", PartCount: 2}}, {Metadata: &MessageMetadata{ContentHash: "old", PartCount: 2}}}
	if messagePartsMatch(parts, "new") {
		t.Fatal("partial edit acknowledged")
	}
	parts[1].Metadata = &MessageMetadata{ContentHash: "new", PartCount: 2}
	if !messagePartsMatch(parts, "new") {
		t.Fatal("complete edit not acknowledged")
	}
	if messagePartsMatch(parts[:1], "new") {
		t.Fatal("missing media part acknowledged")
	}
}
