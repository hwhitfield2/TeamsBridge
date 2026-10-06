package connector

import (
	"context"
	"errors"
	"fmt"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"net/url"
	"sort"
	"strings"
	"sync"
	"teamsbridge.local/teamsbridge/internal/graph"
	"time"
)

type ChannelConfig struct {
	TenantID  string `yaml:"tenant_id"`
	TeamID    string `yaml:"team_id"`
	ChannelID string `yaml:"channel_id"`
	Name      string `yaml:"name"`
}

func (s ChannelConfig) portalID() string { return "channel:" + s.TeamID + ":" + s.ChannelID }
func channelPath(portal string) (string, bool) {
	if !strings.HasPrefix(portal, "channel:") {
		return "", false
	}
	parts := strings.SplitN(strings.TrimPrefix(portal, "channel:"), ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}
	return "/teams/" + url.PathEscape(parts[0]) + "/channels/" + url.PathEscape(parts[1]), true
}
func messageResource(portal string, m graph.Message) string {
	path := graph.ChatPath(portal)
	if ch, ok := channelPath(portal); ok {
		path = ch
		if m.ReplyToID != "" {
			return path + "/messages/" + url.PathEscape(m.ReplyToID) + "/replies/" + url.PathEscape(m.ID)
		}
	}
	return path + "/messages/" + url.PathEscape(m.ID)
}
func (c *Client) channelInfo(ctx context.Context, portal string) (*bridgev2.ChatInfo, error) {
	path, ok := channelPath(portal)
	if !ok {
		return nil, fmt.Errorf("invalid channel")
	}
	var channel struct {
		DisplayName string `json:"displayName"`
	}
	if err := c.api.Do(ctx, "GET", path, nil, &channel); err != nil {
		return nil, err
	}
	name := channel.DisplayName
	for _, cfg := range c.main.Config.Channels {
		if cfg.portalID() == portal && cfg.Name != "" {
			name = cfg.Name
		}
	}
	kind := database.RoomTypeDefault
	return &bridgev2.ChatInfo{Name: &name, Type: &kind, CanBackfill: true}, nil
}
func (c *Client) flattenChannel(ctx context.Context, roots []graph.Message) ([]graph.Message, error) {
	var out []graph.Message
	for _, root := range roots {
		out = append(out, root)
		replies := root.Replies
		if root.RepliesNext != "" {
			more, err := graph.List[graph.Message](ctx, c.api, root.RepliesNext)
			if err != nil {
				return nil, err
			}
			replies = append(replies, more...)
		}
		for _, reply := range replies {
			reply.ReplyToID = root.ID
			out = append(out, reply)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out, nil
}
func (c *Client) syncChannel(ctx context.Context, cfg ChannelConfig) error {
	portal := cfg.portalID()
	value, _ := c.chatSync.LoadOrStore(portal, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	if !lock.TryLock() {
		return nil
	}
	defer lock.Unlock()
	path, _ := channelPath(portal)
	info, err := c.channelInfo(ctx, portal)
	if err != nil {
		return err
	}
	if !c.login.QueueRemoteEvent(&simplevent.ChatResync{EventMeta: simplevent.EventMeta{Type: bridgev2.RemoteEventChatResync, PortalKey: c.key(portal), CreatePortal: true}, ChatInfo: info}).Success {
		return fmt.Errorf("could not queue channel discovery")
	}
	var page struct {
		Value []graph.Message `json:"value"`
	}
	if err = c.api.Do(ctx, "GET", path+"/messages?$top=50&$expand=replies", nil, &page); err != nil {
		return err
	}
	messages, err := c.flattenChannel(ctx, page.Value)
	if err != nil {
		return err
	}
	for _, m := range messages {
		if err := c.queueMessage(ctx, portal, m, true); err != nil {
			return err
		}
	}
	return nil
}
func (c *Client) channelLoop(ctx context.Context) {
	if len(c.main.Config.Channels) == 0 {
		return
	}
	for ctx.Err() == nil {
		delay := 15 * time.Second
		for _, cfg := range c.main.Config.Channels {
			if cfg.TenantID == "" || !strings.HasPrefix(string(c.login.ID), cfg.TenantID+":") {
				continue
			}
			if err := c.syncChannel(ctx, cfg); err != nil && ctx.Err() == nil {
				c.login.Log.Warn().Err(err).Msg("Teams channel sync failed")
				delay = time.Minute
				var ge *graph.APIError
				if errors.As(err, &ge) && ge.RetryAfter > delay {
					delay = ge.RetryAfter
				}
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
