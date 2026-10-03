package connector

import (
	"context"
	"errors"
	"fmt"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/status"
	"maunium.net/go/mautrix/event"
	"strings"
	"sync"
	"sync/atomic"
	"teamsbridge.local/teamsbridge/internal/graph"
	"time"
)

type Client struct {
	chatSync      sync.Map
	memberUpdated sync.Map
	recentSeen    map[string]recentStamp
	main          *Connector
	login         *bridgev2.UserLogin
	meta          *Metadata
	api           *graph.Client
	authMu        sync.Mutex
	lifeMu        sync.Mutex
	cancel        context.CancelFunc
	done          chan struct{}
	logged        atomic.Bool
	profileMu     sync.Mutex
	profiles      map[string]profileCacheEntry
}

var _ bridgev2.NetworkAPI = (*Client)(nil)

func (c *Client) token(ctx context.Context) (string, error) {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	c.meta.mu.Lock()
	savedToken := c.meta.Token
	c.meta.mu.Unlock()
	if savedToken.Refresh == "" {
		return "", bridgev2.ErrNotLoggedIn
	}
	if time.Until(savedToken.ExpiresAt) > 2*time.Minute {
		return savedToken.Access, nil
	}
	t, err := c.oauth().Refresh(ctx, savedToken.Refresh)
	if err != nil {
		return "", err
	}
	c.meta.mu.Lock()
	c.meta.Token = *t
	c.meta.mu.Unlock()
	if err = c.login.Save(ctx); err != nil {
		return "", fmt.Errorf("save refreshed credentials: %w", err)
	}
	return t.Access, nil
}
func (c *Client) Connect(ctx context.Context) {
	c.lifeMu.Lock()
	defer c.lifeMu.Unlock()
	if c.cancel != nil {
		return
	}
	bg, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.done = make(chan struct{})
	done := c.done
	go func() { defer close(done); c.loop(bg) }()
}
func (c *Client) Disconnect() {
	c.lifeMu.Lock()
	defer c.lifeMu.Unlock()
	if c.cancel != nil {
		c.cancel()
		<-c.done
		c.cancel = nil
	}
	c.logged.Store(false)
}
func (c *Client) IsLoggedIn() bool { return c.logged.Load() }
func (c *Client) LogoutRemote(ctx context.Context) {
	c.Disconnect()
	c.authMu.Lock()
	defer c.authMu.Unlock()
	c.meta.mu.Lock()
	c.meta.Token = graph.Token{}
	c.meta.mu.Unlock()
	if err := c.login.Save(ctx); err != nil {
		c.login.Log.Error().Err(err).Msg("Could not clear saved Teams credentials")
	}
}
func (c *Client) IsThisUser(ctx context.Context, id networkid.UserID) bool {
	return string(id) == c.meta.UserID
}
func (c *Client) GetCapabilities(ctx context.Context, p *bridgev2.Portal) *event.RoomFeatures {
	return &event.RoomFeatures{ID: "teamswork.media.v4", File: event.FileFeatureMap{event.CapMsgGIF: {MimeTypes: map[string]event.CapabilitySupportLevel{"image/gif": event.CapLevelFullySupported, "video/mp4": event.CapLevelPartialSupport}, MaxSize: maxHostedImage, Caption: event.CapLevelFullySupported}, event.CapabilityMsgType(event.MsgImage): {MimeTypes: map[string]event.CapabilitySupportLevel{"image/gif": event.CapLevelFullySupported, "image/png": event.CapLevelFullySupported, "image/jpeg": event.CapLevelFullySupported}, MaxSize: maxHostedImage, Caption: event.CapLevelFullySupported}}, Reply: event.CapLevelFullySupported, Formatting: event.FormattingFeatureMap{event.FmtUserLink: event.CapLevelFullySupported}}
}
func (c *Client) GetUserInfo(ctx context.Context, g *bridgev2.Ghost) (*bridgev2.UserInfo, error) {
	return c.profile(ctx, string(g.ID), g.Name), nil
}
func (c *Client) key(chat string) networkid.PortalKey {
	return networkid.PortalKey{ID: networkid.PortalID(chat), Receiver: c.login.ID}
}
func (c *Client) sender(user string) bridgev2.EventSender {
	result := bridgev2.EventSender{Sender: networkid.UserID(user)}
	if user == c.meta.UserID {
		result.IsFromMe = true
		result.SenderLogin = c.login.ID
	}
	return result
}
func (c *Client) chatInfo(chat graph.Chat) *bridgev2.ChatInfo {
	name := chat.Topic
	kind := database.RoomTypeDefault
	if chat.ChatType == "oneOnOne" {
		kind = database.RoomTypeDM
	}
	members := &bridgev2.ChatMemberList{IsFull: true, MemberMap: bridgev2.ChatMemberMap{}, ExcludeChangesFromTimeline: true}
	var names []string
	for _, m := range chat.Members {
		if m.UserID == "" {
			continue
		}
		display := m.DisplayName
		members.MemberMap.Set(bridgev2.ChatMember{EventSender: c.sender(m.UserID), Membership: event.MembershipJoin, UserInfo: &bridgev2.UserInfo{Name: &display}})
		if m.UserID != c.meta.UserID {
			names = append(names, display)
		}
	}
	if name == "" {
		name = strings.Join(names, ", ")
	}
	if name == "" {
		name = "Teams chat"
	}
	return &bridgev2.ChatInfo{Name: &name, Type: &kind, Members: members, CanBackfill: true}
}
func (c *Client) GetChatInfo(ctx context.Context, p *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
	var chat graph.Chat
	path := graph.ChatPath(string(p.ID))
	if err := c.api.Do(ctx, "GET", path, nil, &chat); err != nil {
		return nil, err
	}
	members, err := graph.List[graph.Member](ctx, c.api, path+"/members")
	if err != nil {
		return nil, err
	}
	chat.Members = members
	info := c.chatInfo(chat)
	c.enrichMembers(ctx, info)
	return info, nil
}
func messageID(chat, id string) networkid.MessageID { return networkid.MessageID(chat + "/" + id) }
func (c *Client) HandleMatrixMessage(ctx context.Context, msg *bridgev2.MatrixMessage) (*bridgev2.MatrixMessageResponse, error) {
	if msg == nil || msg.Content == nil || (msg.Content.MsgType != event.MsgText && msg.Content.MsgType != event.MsgImage && msg.Content.MsgType != event.MsgFile && msg.Content.MsgType != event.MsgVideo) {
		return nil, bridgev2.ErrUnsupportedMessageType
	}
	if c.meta.Auth != nil && c.meta.Auth.Profile == "readonly" {
		return nil, fmt.Errorf("this login is read-only; sign in with the messaging flow after ChatMessage.Send is approved")
	}
	// bridgev2 serializes remote echoes and local sends within each portal.
	var out graph.Message
	chat := string(msg.Portal.ID)
	var path string
	var payload any
	var err error
	if msg.Content.MsgType != event.MsgText {
		payload, path, err = c.outgoingMedia(ctx, msg)
	} else {
		path, payload, err = outgoingChatMessage(msg)
		if err == nil {
			err = c.addOutgoingMentions(ctx, msg, payload)
		}
	}
	if err != nil {
		return nil, err
	}
	if err := c.api.Do(ctx, "POST", path, payload, &out); err != nil {
		return nil, err
	}
	if out.ID == "" {
		return nil, errors.New("Teams returned a message without an ID; check Teams before retrying")
	}
	return &bridgev2.MatrixMessageResponse{DB: &database.Message{ID: messageID(chat, out.ID), SenderID: networkid.UserID(c.meta.UserID), Timestamp: out.Created}}, nil
}
func (c *Client) loop(ctx context.Context) {
	historyDone := make(chan struct{})
	go func() { defer close(historyDone); c.discoveryLoop(ctx) }()
	defer func() { <-historyDone }()

	c.login.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnecting})
	for ctx.Err() == nil {
		err := c.syncRecent(ctx)
		delay := time.Duration(c.main.Config.PollSeconds) * time.Second
		if err != nil {
			state := status.BridgeState{StateEvent: status.StateTransientDisconnect, Message: err.Error()}
			var oe *graph.OAuthError
			if errors.As(err, &oe) && (oe.Code == "invalid_grant" || oe.Code == "interaction_required") {
				c.logged.Store(false)
				state.StateEvent = status.StateBadCredentials
				state.UserAction = status.UserActionRelogin
			}
			var ge *graph.APIError
			if errors.As(err, &ge) && ge.RetryAfter > delay {
				delay = ge.RetryAfter
			}
			c.login.BridgeState.Send(state)
			c.login.Log.Warn().Err(err).Msg("Teams sync failed")
		} else {
			c.logged.Store(true)
			c.login.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnected})
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
