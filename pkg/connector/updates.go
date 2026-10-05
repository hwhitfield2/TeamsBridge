package connector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"teamsbridge.local/teamsbridge/internal/graph"
)

type MessageMetadata struct {
	RenderingRevision int    `json:"rendering_revision,omitempty"`
	ContentHash       string `json:"content_hash,omitempty"`
	PartCount         int    `json:"part_count,omitempty"`
}

func contentHash(m graph.Message) string {
	data, _ := json.Marshal([]any{m.Body, m.Subject, m.Attachments, m.Mentions})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func stampMessage(out *bridgev2.ConvertedMessage, m graph.Message) {
	for _, p := range out.Parts {
		p.DBMetadata = &MessageMetadata{RenderingRevision: renderingRevision, ContentHash: contentHash(m), PartCount: len(out.Parts)}
	}
}
func (c *Client) reactionData(m graph.Message) *bridgev2.ReactionSyncData {
	out := &bridgev2.ReactionSyncData{Users: map[networkid.UserID]*bridgev2.ReactionSyncUser{}, HasAllUsers: true}
	for _, r := range m.Reactions {
		if r.User.User == nil || r.User.User.ID == "" {
			continue
		}
		uid := networkid.UserID(r.User.User.ID)
		u := out.Users[uid]
		if u == nil {
			u = &bridgev2.ReactionSyncUser{HasAllReactions: true}
			out.Users[uid] = u
		}
		emoji := reactionEmoji(r.Type)
		if r.Type == "custom" {
			emoji = ":" + r.DisplayName + ":"
		}
		u.Reactions = append(u.Reactions, &bridgev2.BackfillReaction{Sender: c.sender(string(uid)), Emoji: emoji, EmojiID: customReactionID(r), Timestamp: r.Created})
	}
	return out
}
func (c *Client) convertEdit(ctx context.Context, p *bridgev2.Portal, intent bridgev2.MatrixAPI, existing []*database.Message, m graph.Message) (*bridgev2.ConvertedEdit, error) {
	converted, err := c.convertMessage(ctx, p, intent, m)
	if err != nil {
		return nil, err
	}
	out := &bridgev2.ConvertedEdit{}
	parts := map[networkid.PartID]*database.Message{}
	for _, part := range existing {
		parts[part.PartID] = part
	}
	// Validate before ToEditPart mutates any metadata. Matrix cannot insert
	// additional events at an existing message's position in this timeline.
	for _, part := range converted.Parts {
		if parts[part.ID] == nil {
			return nil, fmt.Errorf("cannot insert additional parts into an existing Matrix message; rebuild the Beeper chat to repair its layout")
		}
	}
	for _, part := range converted.Parts {
		out.ModifiedParts = append(out.ModifiedParts, part.ToEditPart(parts[part.ID]))
		delete(parts, part.ID)
	}

	for _, old := range parts {
		out.DeletedParts = append(out.DeletedParts, old)
	}
	return out, nil
}
func (c *Client) handleExisting(ctx context.Context, p *bridgev2.Portal, intent bridgev2.MatrixAPI, parts []*database.Message, m graph.Message) (bridgev2.UpsertResult, error) {
	result := bridgev2.UpsertResult{}
	if len(parts) == 0 {
		return result, nil
	}
	old, _ := parts[0].Metadata.(*MessageMetadata)
	hash := contentHash(m)

	repairHTML := needsBotHTMLRepair(parts, m)
	if messagePartsMatch(parts, hash) && !repairHTML {
		return result, nil
	}
	if (old == nil || old.ContentHash == "") && m.Edited == nil && !repairHTML {
		for _, part := range parts {
			part.Metadata = &MessageMetadata{ContentHash: hash, PartCount: len(parts)}
		}
		result.SaveParts = true
		return result, nil
	}
	result.SubEvents = []bridgev2.RemoteEvent{&simplevent.Message[graph.Message]{EventMeta: simplevent.EventMeta{Type: bridgev2.RemoteEventEdit, PortalKey: p.PortalKey, Sender: c.sender(m.From.User.ID), Timestamp: m.Modified}, TargetMessage: messageID(string(p.ID), m.ID), Data: m, ConvertEditFunc: c.convertEditQuietly}}
	return result, nil
}

// Wait for durable message state before advancing the Graph cursor, including edits
// and deletions. A failed delivery is replayed on the next poll.
func (c *Client) queueMessage(ctx context.Context, portal string, m graph.Message, wait bool) error {
	m = c.normalizeSender(m)
	if m.ID == "" {
		return nil
	}
	mid := messageID(portal, m.ID)
	old, err := c.main.Bridge.DB.Message.GetLastPartByID(ctx, c.login.ID, mid)
	if err != nil {
		return err
	}
	done := make(chan struct{})
	post := func(context.Context, *bridgev2.Portal) { close(done) }
	var evt bridgev2.RemoteEvent
	if m.Deleted != nil {
		if old == nil {
			return nil
		}
		evt = &simplevent.MessageRemove{EventMeta: simplevent.EventMeta{PostHandleFunc: post, Type: bridgev2.RemoteEventMessageRemove, PortalKey: c.key(portal), Sender: c.sender(string(old.SenderID)), Timestamp: *m.Deleted}, TargetMessage: mid}
	} else {
		if m.From.User == nil || m.From.User.ID == "" || m.MessageType != "message" {
			return nil
		}
		evt = &simplevent.Message[graph.Message]{EventMeta: simplevent.EventMeta{PostHandleFunc: post, Type: bridgev2.RemoteEventMessageUpsert, PortalKey: c.key(portal), CreatePortal: true, Sender: c.sender(m.From.User.ID), Timestamp: m.Created, StreamOrder: m.Created.UnixMilli()}, ID: mid, Data: m, ConvertMessageFunc: c.convertMessageQuietly, HandleExistingFunc: c.handleExisting}
	}
	if !c.login.QueueRemoteEvent(evt).Success {
		return fmt.Errorf("could not queue Teams message update")
	}
	if wait {
		deadline, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		select {
		case <-deadline.Done():
			return fmt.Errorf("waiting for Teams delivery: %w", deadline.Err())
		case <-done:
		}
		rows, err := c.main.Bridge.DB.Message.GetAllPartsByID(ctx, c.login.ID, mid)
		if err != nil {
			return err
		}
		delivered := m.Deleted != nil && len(rows) == 0
		if m.Deleted == nil {
			delivered = messagePartsMatch(rows, contentHash(m)) && !needsBotHTMLRepair(rows, m)
		}

		if !delivered {
			return fmt.Errorf("Teams message update was not saved; retrying next poll")
		}
	}

	if m.Deleted == nil && m.Reactions != nil {
		portalObj, err := c.main.Bridge.GetExistingPortalByKey(ctx, c.key(portal))
		if err != nil {
			return err
		}
		if portalObj == nil {
			return fmt.Errorf("missing reaction portal")
		}
		expected, err := c.reactionDataWithMedia(ctx, portalObj, c.main.Bridge.Bot, m)
		if err != nil {
			return err
		}
		synced, err := c.reactionsMatch(ctx, mid, expected)
		if err != nil {
			return err
		}
		if synced {
			return nil
		}
		reactionDone := make(chan struct{})
		evt := &simplevent.ReactionSync{EventMeta: simplevent.EventMeta{Type: bridgev2.RemoteEventReactionSync, PortalKey: c.key(portal), PostHandleFunc: func(context.Context, *bridgev2.Portal) { close(reactionDone) }}, TargetMessage: mid, Reactions: expected}
		if !c.login.QueueRemoteEvent(evt).Success {
			return fmt.Errorf("could not queue Teams reactions")
		}
		if wait {
			timer := time.NewTimer(90 * time.Second)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return fmt.Errorf("waiting for Teams reactions")
			case <-reactionDone:
			}
			synced, err = c.reactionsMatch(ctx, mid, expected)
			if err != nil {
				return err
			}
			if !synced {
				return fmt.Errorf("Teams reactions were not saved; retrying next poll")
			}
		}
	}

	return nil
}

func (c *Client) normalizeSender(m graph.Message) graph.Message {
	if m.From.User == nil && m.From.Application != nil && m.From.Application.ID != "" {
		user := *m.From.Application
		user.ID = "app-" + user.ID
		if user.DisplayName == "" {
			user.DisplayName = "Teams app"
		}
		c.appNames.Store(user.ID, user.DisplayName)
		m.From.User = &user
	}
	return m
}

func (c *Client) reactionsMatch(ctx context.Context, mid networkid.MessageID, expected *bridgev2.ReactionSyncData) (bool, error) {
	rows, err := c.main.Bridge.DB.Reaction.GetAllToMessage(ctx, c.login.ID, mid)
	if err != nil {
		return false, err
	}
	actual := map[string]bool{}
	for _, r := range rows {
		actual[string(r.SenderID)+"\x00"+string(r.EmojiID)] = true
	}
	want := map[string]bool{}
	for uid, u := range expected.Users {
		for _, r := range u.Reactions {
			want[string(uid)+"\x00"+string(r.EmojiID)] = true
		}
	}
	if len(actual) != len(want) {
		return false, nil
	}
	for k := range want {
		if !actual[k] {
			return false, nil
		}
	}
	return true, nil
}

func messagePartsMatch(parts []*database.Message, hash string) bool {
	if len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		meta, _ := part.Metadata.(*MessageMetadata)
		if meta == nil || meta.ContentHash != hash || (meta.PartCount > 0 && meta.PartCount != len(parts)) {
			return false
		}
	}
	return true
}
