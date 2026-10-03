package connector

import (
	"encoding/json"
	"fmt"
	"strings"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"teamsbridge.local/teamsbridge/internal/graph"
)

func (c *Client) convertChatMessage(p *bridgev2.Portal, m graph.Message) *bridgev2.ConvertedMessage {
	m, mentions := c.prepareMentions(m)
	out := convert(m)
	for _, a := range m.Attachments {
		if a.ContentType != "messageReference" {
			continue
		}
		var ref struct {
			MessageID      string `json:"messageId"`
			MessagePreview string `json:"messagePreview"`
			MessageSender  struct {
				User *graph.User `json:"user"`
			} `json:"messageSender"`
		}
		if json.Unmarshal([]byte(a.Content), &ref) != nil || ref.MessageID == "" || ref.MessageID == m.ID {
			continue
		}
		if out.ReplyTo == nil {
			out.ReplyTo = &networkid.MessageOptionalPartID{MessageID: messageID(string(p.ID), ref.MessageID)}
			out.ReplyToRoom = p.PortalKey
			if ref.MessageSender.User != nil {
				out.ReplyToUser = networkid.UserID(ref.MessageSender.User.ID)
				if ref.MessageSender.User.ID == c.meta.UserID {
					out.ReplyToLogin = p.Receiver
				}
			}
		}
		// Keep a readable quote when the original has not yet been backfilled,
		// and preserve additional references (Matrix supports one reply target).
		if ref.MessagePreview != "" {
			out.Parts[0].Content.Body = "> " + strings.ReplaceAll(ref.MessagePreview, "\n", "\n> ") + "\n\n" + out.Parts[0].Content.Body
		}
	}
	applyMentions(out.Parts[0].Content, mentions)
	return out
}

func outgoingChatMessage(msg *bridgev2.MatrixMessage) (string, any, error) {
	chat := string(msg.Portal.ID)
	path := graph.ChatPath(chat) + "/messages"
	message := map[string]any{"body": map[string]string{"contentType": "text", "content": msg.Content.Body}}
	target := msg.ReplyTo
	if target == nil {
		target = msg.ThreadRoot
	}
	if target == nil {
		if rel := msg.Content.RelatesTo; rel != nil && (rel.GetReplyTo() != "" || rel.GetThreadParent() != "") {
			return "", nil, fmt.Errorf("reply target is not bridged; wait for its history to import")
		}
		return path, message, nil
	}
	prefix := chat + "/"
	if !strings.HasPrefix(string(target.ID), prefix) {
		return "", nil, fmt.Errorf("cannot reply to a message from another Teams chat")
	}
	remoteID := strings.TrimPrefix(string(target.ID), prefix)
	if remoteID == "" || strings.Contains(remoteID, "/") {
		return "", nil, fmt.Errorf("invalid Teams reply target")
	}
	return path + "/replyWithQuote", map[string]any{"messageIds": []string{remoteID}, "replyMessage": message}, nil
}
