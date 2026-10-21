package connector

import (
	"encoding/json"
	"html"
	"maunium.net/go/mautrix/format"
	"net/url"
	"strings"
)

func safeLink(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return ""
	}
	return u.String()
}

// Cards are reduced to safe Matrix HTML. Actions remain links or Teams-only
// placeholders; no card input, script, or remote image is executed.
func renderCard(raw string) string { return format.HTMLToText(renderCardHTML(raw)) }

func renderCardHTML(raw string) string {
	if len(raw) > 1<<20 {
		return "[Card too large — open Teams]"
	}
	var card any
	if json.Unmarshal([]byte(raw), &card) != nil {
		return "[Card — open Teams]"
	}
	nodes := 0
	var walk func(any, int) string
	markdown := func(s string) string {
		c := format.RenderMarkdown(s, true, false)
		if c.FormattedBody == "" {
			return "<p>" + plainHTML(c.Body) + "</p>"
		}
		return matrixHTML(c.FormattedBody)
	}
	walk = func(v any, depth int) string {
		nodes++
		if depth > 20 || nodes > 1000 {
			return ""
		}
		switch x := v.(type) {
		case string:
			return html.EscapeString(x)
		case []any:
			var out strings.Builder
			for _, child := range x {
				out.WriteString(walk(child, depth+1))
			}
			return strings.ReplaceAll(out.String(), "</code></pre><pre><code>", "\n")
		case map[string]any:
			str := func(k string) string { s, _ := x[k].(string); return s }
			typ := str("type")
			if strings.HasPrefix(typ, "Input.") {
				return "<p>[Input — open Teams]</p>"
			}
			if strings.HasPrefix(typ, "Action.") || typ == "openUrl" {
				title := str("title")
				if title == "" {
					title = "Open Teams"
				}
				if typ == "Action.OpenUrl" || typ == "openUrl" {
					u := str("url")
					if typ == "openUrl" {
						u = str("value")
					}
					if link := safeLink(u); link != "" {
						return "<p><a href=\"" + html.EscapeString(link) + "\">" + html.EscapeString(title) + "</a></p>"
					}
					return ""
				}
				return "<p>" + html.EscapeString(title) + " [Action — open Teams]</p>"
			}
			switch typ {
			case "TextRun":
				t := html.EscapeString(str("text"))
				if str("fontType") == "monospace" {
					t = "<code>" + t + "</code>"
				}
				if str("weight") == "bolder" {
					t = "<strong>" + t + "</strong>"
				}
				if x["italic"] == true {
					t = "<em>" + t + "</em>"
				}
				if x["strikethrough"] == true {
					t = "<del>" + t + "</del>"
				}
				return t
			case "RichTextBlock":
				inlines, _ := x["inlines"].([]any)
				code := len(inlines) > 0
				var text strings.Builder
				for _, inline := range inlines {
					run, ok := inline.(map[string]any)
					if !ok || run["fontType"] != "monospace" {
						code = false
						break
					}
					value, _ := run["text"].(string)
					text.WriteString(value)
				}
				if code {
					return "<pre><code>" + html.EscapeString(text.String()) + "</code></pre>"
				}
				return "<p>" + walk(x["inlines"], depth+1) + "</p>"
			case "TextBlock":
				t := markdown(str("text"))
				if str("weight") == "bolder" {
					// Wrap blocks in a container that remains valid for multi-paragraph text.
					t = strings.ReplaceAll(t, "<p>", "<p><strong>")
					t = strings.ReplaceAll(t, "</p>", "</strong></p>")
					if !strings.Contains(t, "<p>") {
						t = "<strong>" + t + "</strong>"
					}
				}
				if x["separator"] == true {
					t = "<hr>" + t
				}
				return t
			case "FactSet":
				facts, _ := x["facts"].([]any)
				var out strings.Builder
				for _, f := range facts {
					nodes++
					if nodes > 1000 {
						break
					}
					fact, ok := f.(map[string]any)
					if !ok {
						continue
					}
					title, _ := fact["title"].(string)
					value, _ := fact["value"].(string)
					valueHTML := markdown(value)
					label := "<strong>" + html.EscapeString(strings.TrimSuffix(title, ":")) + ":</strong> "
					if strings.HasPrefix(valueHTML, "<p>") {
						valueHTML = "<p>" + label + strings.TrimPrefix(valueHTML, "<p>")
					} else {
						valueHTML = "<p>" + label + "</p>" + valueHTML
					}
					out.WriteString(valueHTML)
				}
				return out.String()
			}
			var out strings.Builder
			if x["separator"] == true {
				out.WriteString("<hr>")
			}
			for _, key := range []string{"title", "subtitle", "text", "value"} {
				if s := str(key); strings.TrimSpace(s) != "" {
					out.WriteString(markdown(s))
				}
			}
			for _, key := range []string{"body", "items", "columns", "facts", "actions", "buttons", "inlines"} {
				if child, ok := x[key]; ok {
					out.WriteString(walk(child, depth+1))
				}
			}
			return out.String()
		}
		return ""
	}
	result := walk(card, 0)
	if result == "" {
		return "[Rich card — open Teams]"
	}
	return result
}
