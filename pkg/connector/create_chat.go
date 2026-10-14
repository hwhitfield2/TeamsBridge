package connector

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"teamsbridge.local/teamsbridge/internal/graph"
)

func supportedChatType(kind string) bool {
	return kind == "oneOnOne" || kind == "group" || kind == "meeting"
}
func (c *Client) ResolveIdentifier(ctx context.Context, identifier string, createChat bool) (*bridgev2.ResolveIdentifierResponse, error) {
	var user graph.User
	if err := c.api.Do(ctx, "GET", "/users/"+url.PathEscape(strings.TrimSpace(identifier))+"?$select=id,displayName", nil, &user); err != nil {
		return nil, err
	}
	if user.ID == "" {
		return nil, fmt.Errorf("Teams user was not found")
	}
	// The provisioning adapter only includes the contact MXID when Ghost is
	// present. Returning UserID/UserInfo alone loses recipient identity even
	// when DMRoomID and the Teams destination are correct.
	ghost, err := c.main.Bridge.GetGhostByID(ctx, networkid.UserID(user.ID))
	if err != nil {
		return nil, fmt.Errorf("resolve Beeper contact: %w", err)
	}
	out := &bridgev2.ResolveIdentifierResponse{Ghost: ghost, UserID: networkid.UserID(user.ID), UserInfo: &bridgev2.UserInfo{Name: &user.DisplayName}}
	if createChat {
		chat, err := c.createChat(ctx, "oneOnOne", "", []networkid.UserID{out.UserID})
		if err != nil {
			return nil, err
		}
		out.Chat = chat
	}
	return out, nil
}
func (c *Client) CreateGroup(ctx context.Context, p *bridgev2.GroupCreateParams) (*bridgev2.CreateChatResponse, error) {
	if p.Type != "" && p.Type != "group" {
		return nil, fmt.Errorf("only Teams group chats can be created")
	}
	if p.Parent != nil || p.Disappear != nil || p.Avatar != nil || p.Username != "" {
		return nil, fmt.Errorf("unsupported Teams group options")
	}
	name := ""
	if p.Name != nil {
		name = p.Name.Name
	}
	if name == "" && p.Topic != nil {
		name = p.Topic.Topic
	}
	return c.createChat(ctx, "group", name, p.Participants)
}
func (c *Client) createChat(ctx context.Context, kind, name string, users []networkid.UserID) (*bridgev2.CreateChatResponse, error) {
	if err := c.requireWrite("Chat.Create", "Chat.ReadWrite"); err != nil {
		return nil, err
	}
	seen := map[string]bool{c.meta.UserID: true}
	ids := []string{c.meta.UserID}
	for _, u := range users {
		id := string(u)
		if id == "" || strings.ContainsAny(id, "/'?#") {
			return nil, fmt.Errorf("invalid Teams participant")
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) < 2 || len(ids) > 250 || (kind == "oneOnOne" && len(ids) != 2) {
		return nil, fmt.Errorf("invalid participant count")
	}
	members := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		members = append(members, map[string]any{"@odata.type": "#microsoft.graph.aadUserConversationMember", "roles": []string{"owner"}, "user@odata.bind": graph.BaseURL + "/users('" + id + "')"})
	}
	body := map[string]any{"chatType": kind, "members": members}
	if kind == "group" && name != "" {
		body["topic"] = name
	}
	var chat graph.Chat
	if err := c.api.Do(ctx, "POST", "/chats", body, &chat); err != nil {
		return nil, err
	}
	if chat.ID == "" {
		return nil, fmt.Errorf("Teams returned no chat ID; check Teams before retrying")
	}
	return &bridgev2.CreateChatResponse{PortalKey: c.key(chat.ID)}, nil
}

var _ bridgev2.GroupCreatingNetworkAPI = (*Client)(nil)

// Preserve the already-resolved ghost in bridgev2's provisioning response when
// starting a DM from a contact picker or accepting a Matrix ghost invitation.
func (c *Client) CreateChatWithGhost(ctx context.Context, ghost *bridgev2.Ghost) (*bridgev2.CreateChatResponse, error) {
	if ghost == nil || ghost.ID == "" {
		return nil, fmt.Errorf("missing Teams contact")
	}
	return c.createChat(ctx, "oneOnOne", "", []networkid.UserID{ghost.ID})
}

var _ bridgev2.GhostDMCreatingNetworkAPI = (*Client)(nil)
