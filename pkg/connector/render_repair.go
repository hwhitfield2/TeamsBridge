package connector

import (
	"context"
	"sync"
	"teamsbridge.local/teamsbridge/internal/graph"
)

// Once per renderer revision, revisit media in the newest 50 chats without
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
		repair := hasImages(m) || len(m.Attachments) > 0
		for _, r := range m.Reactions {
			repair = repair || r.Type == "custom"
		}
		if !repair {
			continue
		}
		if err = c.queueMessage(ctx, chat.ID, m, true); err != nil {
			return err
		}
	}
	return nil
}
