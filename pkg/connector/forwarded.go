package connector

import (
	"encoding/json"
	"html"
	"strings"

	xhtml "golang.org/x/net/html"
	"teamsbridge.local/teamsbridge/internal/graph"
)

func hasForwardedMessage(m graph.Message) bool {
	for _, a := range m.Attachments {
		if a.ContentType == "forwardedMessageReference" {
			return true
		}
	}
	return false
}

// Forwarded content is embedded in the attachment; its hosted images belong to
// the current message. Never fetch the original conversation to render a forward.
func expandForwardedMessages(m graph.Message) graph.Message {
	replacements := map[string]string{}
	attachments := m.Attachments[:0:0]
	for _, a := range m.Attachments {
		if a.ContentType != "forwardedMessageReference" {
			attachments = append(attachments, a)
			continue
		}
		var ref struct {
			Content string `json:"originalMessageContent"`
		}
		rendered := "<p>[Forwarded Teams message — open Teams to view it]</p>"
		if json.Unmarshal([]byte(a.Content), &ref) == nil && strings.TrimSpace(ref.Content) != "" {
			// Teams includes literal escaped newlines inside this otherwise decoded HTML.
			rendered = "<p>Forwarded message</p>" + strings.ReplaceAll(ref.Content, `\n`, "\n")
		}
		replacements[a.ID] = rendered
	}
	if len(replacements) == 0 {
		return m
	}
	var body strings.Builder
	z := xhtml.NewTokenizer(strings.NewReader(m.Body.Content))
	for {
		typ := z.Next()
		if typ == xhtml.ErrorToken {
			break
		}
		raw := string(z.Raw())
		tok := z.Token()
		if (typ == xhtml.StartTagToken || typ == xhtml.SelfClosingTagToken) && tok.Data == "attachment" {
			replaced := false
			for _, attr := range tok.Attr {
				if attr.Key == "id" {
					if content, ok := replacements[attr.Val]; ok {
						body.WriteString(content)
						delete(replacements, attr.Val)
						replaced = true
					}
				}
			}
			if replaced {
				continue
			}
		}
		if m.Body.ContentType != "html" {
			raw = html.EscapeString(raw)
		}
		body.WriteString(raw)
	}
	// Preserve attachment order if Teams omitted its inline placeholder.
	for _, a := range m.Attachments {
		if content, ok := replacements[a.ID]; ok {
			body.WriteString(content)
			delete(replacements, a.ID)
		}
	}
	m.Body.Content = body.String()
	m.Body.ContentType = "html"
	m.Attachments = attachments
	return m
}
