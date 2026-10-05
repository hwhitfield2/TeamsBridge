package connector

import (
	"context"
	"errors"
	"fmt"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"net/url"
	"sort"
	"strings"
	"sync"
	"teamsbridge.local/teamsbridge/internal/graph"
	"time"
)

func (c *Client) sync(ctx context.Context) error {
	chats, err := graph.List[graph.Chat](ctx, c.api, "/me/chats?$top=50&$orderby=lastMessagePreview/createdDateTime%20desc")
	if err != nil {
		return err
	}

	// Prioritize ready history tasks in the same newest-chat-first order. Preserve
	// future retry deadlines and in-flight tasks so this never defeats backoff.
	now := time.Now()
	for rank, chat := range chats {
		priority := now.Add(-time.Hour).UnixNano() + int64(rank)
		_, err = c.main.Bridge.DB.Exec(ctx, `UPDATE backfill_task SET next_dispatch_min_ts=$1 WHERE bridge_id=$2 AND portal_id=$3 AND portal_receiver=$4 AND is_done=false AND next_dispatch_min_ts<$5 AND (dispatched_at IS NULL OR completed_at IS NOT NULL)`, priority, c.main.Bridge.ID, chat.ID, c.login.ID, now.UnixNano())
		if err != nil {
			return fmt.Errorf("prioritize history queue: %w", err)
		}
	}
	c.logged.Store(true)
	c.main.Bridge.WakeupBackfillQueue()
	var firstErr error
	for _, chat := range chats {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !supportedChatType(chat.ChatType) {
			continue
		}
		if err = c.syncChat(ctx, chat); err != nil {
			if errors.Is(err, errChatSyncBusy) {
				continue
			}
			if firstErr == nil {
				firstErr = err
			}
			if ge, ok := err.(*graph.APIError); ok && ge.Status == 429 {
				return err
			}
			c.login.Log.Warn().Err(err).Msg("Could not sync a Teams chat")
		}
	}
	return firstErr
}

var errChatSyncBusy = errors.New("chat already syncing")

func (c *Client) syncChat(ctx context.Context, chat graph.Chat) error {
	lockValue, _ := c.chatSync.LoadOrStore(chat.ID, &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	if !lock.TryLock() {
		return errChatSyncBusy
	}
	defer lock.Unlock()
	updated, ok := c.memberUpdated.Load(chat.ID)
	if !ok || time.Since(updated.(time.Time)) > 5*time.Minute {

		members, err := graph.List[graph.Member](ctx, c.api, graph.ChatPath(chat.ID)+"/members")
		if err != nil {
			return err
		}
		chat.Members = members
		info := c.chatInfo(chat)
		c.enrichMembers(ctx, info)
		result := c.login.QueueRemoteEvent(&simplevent.ChatResync{EventMeta: simplevent.EventMeta{Type: bridgev2.RemoteEventChatResync, PortalKey: c.key(chat.ID), CreatePortal: true}, ChatInfo: info})
		if !result.Success {
			return fmt.Errorf("could not queue Teams chat discovery")
		}
		c.memberUpdated.Store(chat.ID, time.Now())
	}
	c.authMu.Lock()
	c.meta.mu.Lock()
	cursor := c.meta.Cursors[chat.ID]
	c.meta.mu.Unlock()
	c.authMu.Unlock()
	query := url.Values{"$top": {"50"}, "$orderby": {"lastModifiedDateTime desc"}}
	if !cursor.IsZero() {
		query.Set("$filter", "lastModifiedDateTime gt "+cursor.Add(-2*time.Minute).UTC().Format(time.RFC3339Nano))
	}
	path := graph.ChatPath(chat.ID) + "/messages?" + query.Encode()
	var messages []graph.Message
	if cursor.IsZero() {
		var page struct {
			Value []graph.Message `json:"value"`
		}
		if err := c.api.Do(ctx, "GET", path, nil, &page); err != nil {
			return err
		}
		messages = page.Value
		if len(messages) > c.main.Config.InitialMessages {
			messages = messages[:c.main.Config.InitialMessages]
		}
	} else {
		var err error
		messages, err = graph.List[graph.Message](ctx, c.api, path)
		if err != nil {
			return err
		}
	}
	sort.SliceStable(messages, func(i, j int) bool { return messages[i].Created.Before(messages[j].Created) })
	next := cursor
	for _, m := range messages {
		if m.Modified.After(next) {
			next = m.Modified
		}
		if m.Created.After(next) {
			next = m.Created
		}
		if err := c.queueMessage(ctx, chat.ID, m, true); err != nil {
			return err
		}
	}
	if next.IsZero() {
		next = time.Now().Add(-2 * time.Minute)
	}
	c.authMu.Lock()
	defer c.authMu.Unlock()
	c.meta.mu.Lock()
	if c.meta.Cursors == nil {
		c.meta.Cursors = map[string]time.Time{}
	}
	c.meta.Cursors[chat.ID] = next
	c.meta.mu.Unlock()
	if err := c.login.Save(ctx); err != nil {
		c.meta.mu.Lock()
		c.meta.Cursors[chat.ID] = cursor
		c.meta.mu.Unlock()
		return err
	}
	return nil
}
func convert(m graph.Message) *bridgev2.ConvertedMessage {
	body := m.Body.Content
	formatted := ""
	if strings.EqualFold(m.Body.ContentType, "html") {
		formatted = matrixHTML(body)
		body = format.HTMLToText(formatted)
	}
	baseBody := body
	for _, a := range m.Attachments {
		if a.ContentType == "messageReference" {
			continue
		}
		if strings.Contains(a.ContentType, "card") {
			body += "\n" + renderCard(a.Content)
			continue
		}
		name := strings.TrimSpace(a.Name)
		if name == "" {
			name = "Teams attachment — open Teams to view it"
		}
		body += "\n[Attachment: " + name + "]"
		u, err := url.Parse(a.ContentURL)
		if err == nil && u.Scheme == "https" && u.Host != "" {
			body += " " + u.String()
		}
	}
	if strings.TrimSpace(body) == "" {
		body = "[Teams message: open Teams to view this content]"
	}
	content := &event.MessageEventContent{MsgType: event.MsgText, Body: body}
	if formatted != "" {
		content.Format = event.FormatHTML
		content.FormattedBody = formatted + plainHTML(strings.TrimPrefix(body, baseBody))
	}
	return &bridgev2.ConvertedMessage{Parts: []*bridgev2.ConvertedMessagePart{{Type: event.EventMessage, Content: content}}}
}
