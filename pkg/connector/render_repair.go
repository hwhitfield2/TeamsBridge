package connector

import (
	"context"
	"maunium.net/go/mautrix/bridgev2/database"
	"strings"
	"sync"
	"teamsbridge.local/teamsbridge/internal/graph"
)

// Once per renderer revision, revisit media and bot HTML in 50 recent chats without
// resetting history cursors. Configured channels are already revisited each poll.
func (c *Client) repairRecentRendering(ctx context.Context) error {
	c.meta.mu.Lock()
	revision := c.meta.RenderingRevision
	c.meta.mu.Unlock()
	if revision >= renderingRevision {
		return nil
	}
	var chats struct {
		Value []graph.Chat `json:"value"`
	}
	if err := c.api.Do(ctx, "GET", "/me/chats?$top=50&$orderby=lastMessagePreview/createdDateTime%20desc", nil, &chats); err != nil {
		return err
	}
	for _, chat := range chats.Value {
		if err := c.repairChatRendering(ctx, chat); err != nil {
			return err
		}
	}
	c.authMu.Lock()
	defer c.authMu.Unlock()
	c.meta.mu.Lock()
	c.meta.RenderingRevision = renderingRevision
	c.meta.mu.Unlock()
	return c.login.Save(ctx)
}
func (c *Client) repairChatRendering(ctx context.Context, chat graph.Chat) error {
	portal, err := c.main.Bridge.GetExistingPortalByKey(ctx, c.key(chat.ID))
	if err != nil || portal == nil || portal.MXID == "" {
		return err
	}
	value, _ := c.chatSync.LoadOrStore(chat.ID, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()
	var page struct {
		Value []graph.Message `json:"value"`
	}
	if err = c.api.Do(ctx, "GET", graph.ChatPath(chat.ID)+"/messages?$top=50", nil, &page); err != nil {
		return err
	}
	for _, m := range page.Value {
		repair := hasImages(m) || len(m.Attachments) > 0 || repairableBotHTML(m)
		for _, r := range m.Reactions {
			repair = repair || r.Type == "custom"
		}
		if !repair {
			continue
		}
		// Repairs must not introduce older bot messages at the live end of a
		// timeline. Missing messages are imported by normal history backfill.
		if repairableBotHTML(m) {
			existing, lookupErr := c.main.Bridge.DB.Message.GetFirstPartByID(ctx, c.login.ID, messageID(chat.ID, m.ID))
			if lookupErr != nil {
				return lookupErr
			}
			if existing == nil {
				continue
			}
		}
		if err = c.queueMessage(ctx, chat.ID, m, true); err != nil {
			return err
		}
	}
	return nil
}

// Only automatically repair text-only bot messages. Their single event can be
// edited in place without changing historical media layout or inserting parts.
func repairableBotHTML(m graph.Message) bool {
	return m.From.Application != nil && strings.EqualFold(m.Body.ContentType, "html") && len(m.Attachments) == 0 && !hasImages(m)
}
func needsBotHTMLRepair(parts []*database.Message, m graph.Message) bool {
	if !repairableBotHTML(m) || len(parts) != 1 || parts[0].PartID != "" {
		return false
	}
	old, _ := parts[0].Metadata.(*MessageMetadata)
	return old == nil || old.RenderingRevision < 7
}
