package connector

import (
	"context"
	"errors"
	"teamsbridge.local/teamsbridge/internal/graph"
	"time"
)

type recentStamp struct {
	id      string
	checked time.Time
}

func needsRecentSync(chat graph.Chat, previous recentStamp, now time.Time) bool {
	return previous.checked.IsZero() || chat.LastMessage == nil || chat.LastMessage.ID != previous.id || now.Sub(previous.checked) >= time.Minute
}
func (c *Client) syncRecent(ctx context.Context) error {
	var page struct {
		Value []graph.Chat `json:"value"`
	}
	// This lane deliberately reads only the newest page: full discovery and older
	// history run independently, so hundreds of inactive chats cannot delay it.
	if err := c.api.Do(ctx, "GET", "/me/chats?$top=50&$orderby=lastMessagePreview/createdDateTime%20desc&$expand=lastMessagePreview", nil, &page); err != nil {
		return err
	}
	if c.recentSeen == nil {
		c.recentSeen = map[string]recentStamp{}
	}
	var firstErr error
	for _, chat := range page.Value {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if chat.ChatType != "oneOnOne" && chat.ChatType != "group" {
			continue
		}
		if !needsRecentSync(chat, c.recentSeen[chat.ID], time.Now()) {
			continue
		}
		err := c.syncChat(ctx, chat)
		if errors.Is(err, errChatSyncBusy) {
			continue
		}
		if err != nil {
			var ge *graph.APIError
			if errors.As(err, &ge) && ge.Status == 429 {
				return err
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		stamp := recentStamp{checked: time.Now()}
		if chat.LastMessage != nil {
			stamp.id = chat.LastMessage.ID
		}
		c.recentSeen[chat.ID] = stamp
	}
	return firstErr
}
func (c *Client) discoveryLoop(ctx context.Context) {
	for ctx.Err() == nil {
		err := c.sync(ctx)
		delay := 10 * time.Minute
		if err != nil && ctx.Err() == nil {
			c.login.Log.Warn().Err(err).Msg("Background Teams discovery failed")
			delay = time.Minute
			var ge *graph.APIError
			if errors.As(err, &ge) && ge.RetryAfter > delay {
				delay = ge.RetryAfter
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
