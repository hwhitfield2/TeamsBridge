package connector

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"teamsbridge.local/teamsbridge/internal/graph"
)

// Scope hints control UI and explain missing consent. Graph remains the authority.
func (c *Client) hasScope(scope string) bool {
	if c.meta == nil {
		return false
	}
	c.meta.mu.Lock()
	token := c.meta.Token.Access
	scopes := c.meta.Token.Scope
	c.meta.mu.Unlock()
	for _, s := range strings.Fields(scopes) {
		if s == scope {
			return true
		}
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var claims struct {
		Scope string `json:"scp"`
	}
	if json.Unmarshal(data, &claims) != nil {
		return false
	}
	for _, s := range strings.Fields(claims.Scope) {
		if s == scope {
			return true
		}
	}
	return false
}
func (c *Client) requireWrite(scopes ...string) error {
	if c.meta.Auth != nil && c.meta.Auth.Profile == "readonly" {
		return fmt.Errorf("this login is read-only")
	}
	for _, s := range scopes {
		if c.hasScope(s) {
			return nil
		}
	}
	return fmt.Errorf("Microsoft sign-in needs approved delegated %s; sign in again after consent", strings.Join(scopes, " or "))
}
func remoteMessageID(portal string, id networkid.MessageID) (string, error) {
	prefix := portal + "/"
	s := strings.TrimPrefix(string(id), prefix)
	if !strings.HasPrefix(string(id), prefix) || s == "" || strings.Contains(s, "/") {
		return "", fmt.Errorf("invalid or cross-room Teams message target")
	}
	return s, nil
}
func dbMessageResource(portal string, target *database.Message) (string, error) {
	if target == nil {
		return "", fmt.Errorf("message has not been bridged")
	}
	mid, err := remoteMessageID(portal, target.ID)
	if err != nil {
		return "", err
	}
	m := graph.Message{ID: mid}
	if _, ok := channelPath(portal); ok && target.ThreadRoot != "" && target.ThreadRoot != target.ID {
		m.ReplyToID, err = remoteMessageID(portal, target.ThreadRoot)
		if err != nil {
			return "", err
		}
	}
	return messageResource(portal, m), nil
}
func (c *Client) mutationPermission(portal string) error {
	if _, ok := channelPath(portal); ok {
		return c.requireWrite("ChannelMessage.ReadWrite")
	}
	return c.requireWrite("Chat.ReadWrite")
}
func (c *Client) HandleMatrixEdit(ctx context.Context, msg *bridgev2.MatrixEdit) error {
	portal := string(msg.Portal.ID)
	var permissionErr error
	if _, ok := channelPath(portal); ok {
		permissionErr = c.requireWrite("ChannelMessage.Edit", "ChannelMessage.ReadWrite")
	} else {
		permissionErr = c.mutationPermission(portal)
	}
	if permissionErr != nil {
		return permissionErr
	}
	if msg.EditTarget.SenderID != networkid.UserID(c.meta.UserID) {
		return fmt.Errorf("only your own Teams messages can be edited")
	}
	if msg.Content.MsgType != event.MsgText {
		return fmt.Errorf("editing Teams file attachments is not supported")
	}
	path, err := dbMessageResource(portal, msg.EditTarget)
	if err != nil {
		return err
	}
	payload := map[string]any{"body": map[string]string{"contentType": "text", "content": msg.Content.Body}}
	proxy := &bridgev2.MatrixMessage{MatrixEventBase: msg.MatrixEventBase}
	if err = c.addOutgoingMentions(ctx, proxy, payload); err != nil {
		return err
	}
	return c.api.Do(ctx, "PATCH", path, payload, nil)
}
func (c *Client) HandleMatrixMessageRemove(ctx context.Context, msg *bridgev2.MatrixMessageRemove) error {
	portal := string(msg.Portal.ID)
	if err := c.mutationPermission(portal); err != nil {
		return err
	}
	if msg.TargetMessage.SenderID != networkid.UserID(c.meta.UserID) {
		return fmt.Errorf("only your own Teams messages can be deleted")
	}
	path, err := dbMessageResource(portal, msg.TargetMessage)
	if err != nil {
		return err
	}
	if _, ok := channelPath(portal); !ok {
		path = "/users/" + url.PathEscape(c.meta.UserID) + path
	}
	return c.api.Do(ctx, "POST", path+"/softDelete", nil, nil)
}

var legacyReactions = map[string]string{"like": "👍", "heart": "❤️", "laugh": "😆", "surprised": "😮", "sad": "😢", "angry": "😠"}

func reactionEmoji(s string) string {
	if e, ok := legacyReactions[s]; ok {
		return e
	}
	return s
}
func (c *Client) reactionPermission(portal string) error {
	if _, ok := channelPath(portal); ok {
		return c.requireWrite("ChannelMessage.Send")
	}
	return c.requireWrite("ChatMessage.Send", "Chat.ReadWrite")
}
func (c *Client) PreHandleMatrixReaction(ctx context.Context, msg *bridgev2.MatrixReaction) (bridgev2.MatrixReactionPreResponse, error) {
	e := msg.Content.RelatesTo.Key
	if strings.HasPrefix(e, "mxc://") || e == "" {
		return bridgev2.MatrixReactionPreResponse{}, fmt.Errorf("Teams requires a Unicode emoji reaction")
	}
	return bridgev2.MatrixReactionPreResponse{SenderID: networkid.UserID(c.meta.UserID), EmojiID: networkid.EmojiID(e), Emoji: e}, c.reactionPermission(string(msg.Portal.ID))
}
func (c *Client) HandleMatrixReaction(ctx context.Context, msg *bridgev2.MatrixReaction) (*database.Reaction, error) {
	if err := c.reactionPermission(string(msg.Portal.ID)); err != nil {
		return nil, err
	}
	path, err := dbMessageResource(string(msg.Portal.ID), msg.TargetMessage)
	if err != nil {
		return nil, err
	}
	err = c.api.Do(ctx, "POST", path+"/setReaction", map[string]string{"reactionType": msg.PreHandleResp.Emoji}, nil)
	return &database.Reaction{}, err
}
func (c *Client) HandleMatrixReactionRemove(ctx context.Context, msg *bridgev2.MatrixReactionRemove) error {
	if err := c.reactionPermission(string(msg.Portal.ID)); err != nil {
		return err
	}
	if msg.TargetReaction.SenderID != networkid.UserID(c.meta.UserID) {
		return fmt.Errorf("cannot remove another user's Teams reaction")
	}
	if strings.HasPrefix(string(msg.TargetReaction.EmojiID), "custom:") {
		return fmt.Errorf("remove custom image reactions in Teams; Graph only documents Unicode reaction changes")
	}
	target, err := c.main.Bridge.DB.Message.GetLastPartByID(ctx, c.login.ID, msg.TargetReaction.MessageID)
	if err != nil {
		return err
	}
	path, err := dbMessageResource(string(msg.Portal.ID), target)
	if err != nil {
		return err
	}
	return c.api.Do(ctx, "POST", path+"/unsetReaction", map[string]string{"reactionType": reactionEmoji(string(msg.TargetReaction.EmojiID))}, nil)
}
func (c *Client) HandleMatrixReadReceipt(ctx context.Context, msg *bridgev2.MatrixReadReceipt) error {
	if msg.Implicit || msg.ExactMessage == nil {
		return nil
	}
	if _, ok := channelPath(string(msg.Portal.ID)); ok {
		return nil
	}
	if err := c.requireWrite("Chat.ReadWrite"); err != nil {
		return err
	}
	// Graph marks the whole chat read. Don't do that for an older scrolled-to message.
	var page struct {
		Value []graph.Message `json:"value"`
	}
	path := graph.ChatPath(string(msg.Portal.ID))
	if err := c.api.Do(ctx, "GET", path+"/messages?$top=1&$orderby=createdDateTime%20desc", nil, &page); err != nil {
		return err
	}
	if len(page.Value) > 0 && page.Value[0].Created.After(msg.ReadUpTo) {
		return nil
	}
	return c.api.Do(ctx, "POST", path+"/markChatReadForUser", map[string]any{"user": map[string]string{"id": c.meta.UserID, "tenantId": strings.SplitN(string(c.login.ID), ":", 2)[0]}}, nil)
}

var _ bridgev2.EditHandlingNetworkAPI = (*Client)(nil)
var _ bridgev2.RedactionHandlingNetworkAPI = (*Client)(nil)
var _ bridgev2.ReactionHandlingNetworkAPI = (*Client)(nil)
var _ bridgev2.ReadReceiptHandlingNetworkAPI = (*Client)(nil)
