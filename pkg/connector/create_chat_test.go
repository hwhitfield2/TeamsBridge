package connector

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
)

func TestCreateDMWithResolvedGhost(t *testing.T) {
	calls := 0
	c, _ := mockActions(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/v1.0/chats" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var body struct {
			ChatType string           `json:"chatType"`
			Members  []map[string]any `json:"members"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.ChatType != "oneOnOne" || len(body.Members) != 2 || body.Members[0]["user@odata.bind"] != "https://graph.microsoft.com/v1.0/users('self')" || body.Members[1]["user@odata.bind"] != "https://graph.microsoft.com/v1.0/users('recipient')" {
			t.Fatalf("wrong DM participants: %#v", body)
		}
		_, _ = w.Write([]byte(`{"id":"existing-chat"}`))
	})
	c.meta.Token.Scope = "Chat.Create"
	ghost := &bridgev2.Ghost{Ghost: &database.Ghost{ID: "recipient", Name: "Recipient"}}
	result, err := c.CreateChatWithGhost(context.Background(), ghost)
	if err != nil || result.PortalKey.ID != "existing-chat" || result.PortalKey.Receiver != c.login.ID || calls != 1 {
		t.Fatal(result, err, calls)
	}
	// A ghost whose directory lookup is unavailable can still start a DM using
	// its stable ID. No /users lookup should discard the supplied contact.
	if ghost.ID != "recipient" || ghost.Name != "Recipient" {
		t.Fatal("contact changed")
	}
	if _, err = c.CreateChatWithGhost(context.Background(), nil); err == nil {
		t.Fatal("nil contact accepted")
	}
	if _, err = c.CreateChatWithGhost(context.Background(), &bridgev2.Ghost{Ghost: &database.Ghost{ID: "self"}}); err == nil {
		t.Fatal("self selected as recipient")
	}
	if calls != 1 {
		t.Fatal("invalid contacts sent to Graph")
	}
}
