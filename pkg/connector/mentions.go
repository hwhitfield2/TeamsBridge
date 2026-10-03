package connector

import (
	"context"
	"fmt"
	stdhtml "html"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"
	"teamsbridge.local/teamsbridge/internal/graph"
)

type inboundMention struct {
	marker, label string
	user          id.UserID
	room          bool
}

func (c *Client) prepareMentions(m graph.Message) (graph.Message, []inboundMention) {
	if len(m.Mentions) == 0 || !strings.EqualFold(m.Body.ContentType, "html") {
		return m, nil
	}
	prefix := "TEAMSMENTION"
	for strings.Contains(m.Body.Content, prefix) {
		prefix += "X"
	}
	refs := map[string]graph.Mention{}
	for _, ref := range m.Mentions {
		refs[strconv.Itoa(ref.ID)] = ref
	}
	var replacements []inboundMention
	var body strings.Builder
	z := html.NewTokenizer(strings.NewReader(m.Body.Content))
	for {
		typ := z.Next()
		if typ == html.ErrorToken {
			break
		}
		if typ == html.StartTagToken {
			tok := z.Token()
			if tok.Data == "at" {
				key := ""
				for _, a := range tok.Attr {
					if a.Key == "id" {
						key = a.Val
					}
				}
				if ref, ok := refs[key]; ok {
					r := inboundMention{marker: fmt.Sprintf("%s%dEND", prefix, len(replacements)), label: ref.Text}
					if ref.Mentioned.User != nil && ref.Mentioned.User.ID != "" {
						if ref.Mentioned.User.ID == c.meta.UserID {
							r.user = c.login.UserMXID
						} else {
							r.user = c.main.Bridge.Matrix.GhostIntent(networkid.UserID(ref.Mentioned.User.ID)).GetMXID()
						}
					}
					if ref.Mentioned.Conversation != nil && ref.Mentioned.Conversation.Type == "chat" {
						r.room = true
					}
					replacements = append(replacements, r)
					body.WriteString(r.marker)
					for {
						t := z.Next()
						if t == html.ErrorToken {
							break
						}
						if t == html.EndTagToken {
							n, _ := z.TagName()
							if string(n) == "at" {
								break
							}
						}
					}
					continue
				}
			}
			body.WriteString(tok.String())
			continue
		}
		body.Write(z.Raw())
	}
	m.Body.Content = body.String()
	return m, replacements
}
func applyMentions(content *event.MessageEventContent, refs []inboundMention) {
	if len(refs) == 0 {
		return
	}
	formatted := stdhtml.EscapeString(content.Body)
	content.Mentions = &event.Mentions{}
	for _, r := range refs {
		label := "@" + strings.TrimPrefix(r.label, "@")
		rendered := stdhtml.EscapeString(label)
		if r.user != "" {
			rendered = `<a href="https://matrix.to/#/` + stdhtml.EscapeString(string(r.user)) + `">` + rendered + `</a>`
			content.Mentions.Add(r.user)
		}
		if r.room {
			content.Mentions.Room = true
		}
		formatted = strings.ReplaceAll(formatted, r.marker, rendered)
		content.Body = strings.ReplaceAll(content.Body, r.marker, label)
	}
	content.Format = event.FormatHTML
	content.FormattedBody = strings.ReplaceAll(formatted, "\n", "<br>")
}

func outgoingMentions(content *event.MessageEventContent, resolve func(id.UserID) (string, error), implicitReply ...id.UserID) (string, []graph.Mention, error) {
	if content.Mentions != nil && content.Mentions.Room {
		return "", nil, fmt.Errorf("sending @everyone is not supported yet")
	}
	var mentions []graph.Mention
	var conversionErr error
	seen := map[id.UserID]bool{}
	parser := format.HTMLParser{TextConverter: func(s string, _ format.Context) string { return stdhtml.EscapeString(s) }, LinkConverter: func(text, href string, _ format.Context) string {
		return text + " (" + stdhtml.EscapeString(href) + ")"
	}}
	parser.PillConverter = func(name, mxid, eventID string, _ format.Context) string {
		user := id.UserID(mxid)
		if !strings.HasPrefix(mxid, "@") || eventID != "" || (content.Mentions != nil && !content.Mentions.Has(user)) {
			return name
		}
		remote, err := resolve(user)
		if err != nil {
			conversionErr = err
			return name
		}
		ref := graph.Mention{ID: len(mentions), Text: stdhtml.UnescapeString(name)}
		ref.Mentioned.User = &graph.User{ID: remote, DisplayName: ref.Text}
		mentions = append(mentions, ref)
		seen[user] = true
		return fmt.Sprintf(`<at id="%d">%s</at>`, ref.ID, stdhtml.EscapeString(ref.Text))
	}
	body := stdhtml.EscapeString(content.Body)
	if content.Format == event.FormatHTML && content.FormattedBody != "" {
		body = parser.Parse(content.FormattedBody, format.Context{})
	}
	if content.Mentions != nil {
		for _, user := range content.Mentions.UserIDs {
			isReply := false
			for _, implicit := range implicitReply {
				if user == implicit {
					isReply = true
				}
			}
			if !seen[user] && !isReply {
				remote, err := resolve(user)
				if err != nil {
					return "", nil, err
				}
				ref := graph.Mention{ID: len(mentions), Text: string(user)}
				ref.Mentioned.User = &graph.User{ID: remote, DisplayName: ref.Text}
				mentions = append(mentions, ref)
				seen[user] = true
				// Beeper may supply only m.mentions, without a positional HTML pill.
				// Preserve the original text and append an explicit tag for this exact ID.
				body += " " + fmt.Sprintf(`<at id="%d">%s</at>`, ref.ID, stdhtml.EscapeString(ref.Text))
			}
		}
	}
	return strings.ReplaceAll(body, "\n", "<br>"), mentions, conversionErr
}
func (c *Client) addOutgoingMentions(ctx context.Context, msg *bridgev2.MatrixMessage, payload any) error {
	var implicitReply []id.UserID
	if msg.ReplyTo != nil {
		implicitReply = append(implicitReply, msg.ReplyTo.SenderMXID)
		if msg.ReplyTo.SenderID == networkid.UserID(c.meta.UserID) {
			implicitReply = append(implicitReply, c.login.UserMXID)
		} else if msg.ReplyTo.SenderID != "" {
			implicitReply = append(implicitReply, c.main.Bridge.Matrix.GhostIntent(msg.ReplyTo.SenderID).GetMXID())
		}
	}
	body, mentions, err := outgoingMentions(msg.Content, func(user id.UserID) (string, error) {
		if user == c.login.UserMXID {
			return c.meta.UserID, nil
		}
		remote, ok := c.main.Bridge.Matrix.ParseGhostMXID(user)
		if !ok {
			return "", fmt.Errorf("mentioned user is not a bridged Teams user")
		}
		return string(remote), nil
	}, implicitReply...)
	if err != nil {
		return err
	}
	if len(mentions) == 0 {
		return nil
	}
	for i := range mentions {
		ref := &mentions[i]
		if !strings.HasPrefix(ref.Text, "@") {
			continue
		}
		ghost, err := c.main.Bridge.GetGhostByMXID(ctx, id.UserID(ref.Text))
		if err != nil {
			return fmt.Errorf("resolve mentioned user: %w", err)
		}
		name := ""
		if ghost != nil {
			name = ghost.Name
		}
		if name == "" {
			name = ref.Text
		}
		before := fmt.Sprintf(`<at id="%d">%s</at>`, ref.ID, stdhtml.EscapeString(ref.Text))
		after := fmt.Sprintf(`<at id="%d">%s</at>`, ref.ID, stdhtml.EscapeString(name))
		body = strings.ReplaceAll(body, before, after)
		ref.Text = name
		ref.Mentioned.User.DisplayName = name
	}
	message := payload.(map[string]any)
	if reply, ok := message["replyMessage"]; ok {
		message = reply.(map[string]any)
	}
	message["body"] = map[string]string{"contentType": "html", "content": body}
	message["mentions"] = mentions
	return nil
}
