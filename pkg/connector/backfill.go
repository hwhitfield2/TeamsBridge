package connector

import (
	"context"
	"fmt"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"teamsbridge.local/teamsbridge/internal/graph"
	"time"
)

var _ bridgev2.BackfillingNetworkAPI = (*Client)(nil)

func (c *Client) FetchMessages(ctx context.Context, p bridgev2.FetchMessagesParams) (*bridgev2.FetchMessagesResponse, error) {
	channel, isChannel := channelPath(string(p.Portal.ID))
	if p.ThreadRoot != "" && !isChannel {
		return nil, fmt.Errorf("Teams thread backfill is not supported")
	}
	count := p.Count
	if count < 1 || count > 50 {
		count = 50
	}
	path := string(p.Cursor)
	if path == "" || p.Forward {
		q := url.Values{"$top": {strconv.Itoa(count)}, "$orderby": {"createdDateTime desc"}}
		if !p.Forward && p.AnchorMessage != nil {
			q.Set("$filter", "createdDateTime lt "+p.AnchorMessage.Timestamp.UTC().Format(time.RFC3339Nano))
		}
		path = graph.ChatPath(string(p.Portal.ID)) + "/messages?" + q.Encode()
	}
	if isChannel && (string(p.Cursor) == "" || p.Forward) {
		path = channel + "/messages?$top=50&$expand=replies"
		if p.ThreadRoot != "" {
			path = channel + "/messages/" + url.PathEscape(strings.TrimPrefix(string(p.ThreadRoot), string(p.Portal.ID)+"/")) + "/replies?$top=50"
		}
	}
	var page struct {
		Value []graph.Message `json:"value"`
		Next  string          `json:"@odata.nextLink"`
	}
	if err := c.api.Do(ctx, "GET", path, nil, &page); err != nil {
		return nil, err
	}
	if page.Next != "" && page.Next == path {
		return nil, fmt.Errorf("Teams returned a repeating backfill cursor")
	}
	if isChannel {
		var err error
		page.Value, err = c.flattenChannel(ctx, page.Value)
		if err != nil {
			return nil, err
		}
	}
	out := &bridgev2.FetchMessagesResponse{Cursor: networkid.PaginationCursor(page.Next), HasMore: page.Next != "", Forward: p.Forward, MarkRead: true, AggressiveDeduplication: true}
	for _, m := range page.Value {
		m = c.normalizeSender(m)
		if m.ID == "" || m.Deleted != nil || m.From.User == nil || m.From.User.ID == "" || m.MessageType != "message" {
			continue
		}
		converted := c.convertChatMessage(p.Portal, m)
		if hasImages(m) || len(m.Attachments) > 0 {
			var err error
			converted, err = c.convertMessage(ctx, p.Portal, c.main.Bridge.Bot, m)
			if err != nil {
				return nil, err
			}
		}
		reactions := c.reactionData(m)
		for _, reaction := range m.Reactions {
			if reaction.Type == "custom" {
				var err error
				reactions, err = c.reactionDataWithMedia(ctx, p.Portal, c.main.Bridge.Bot, m)
				if err != nil {
					return nil, err
				}
				break
			}
		}
		out.Messages = append(out.Messages, &bridgev2.BackfillMessage{ConvertedMessage: converted, Reactions: reactions.ToBackfill(), Sender: c.sender(m.From.User.ID), ID: messageID(string(p.Portal.ID), m.ID), Timestamp: m.Created, StreamOrder: m.Created.UnixMilli()})
	}
	sort.SliceStable(out.Messages, func(i, j int) bool { return out.Messages[i].Timestamp.Before(out.Messages[j].Timestamp) })
	return out, nil
}
