package connector

import (
	"encoding/json"
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

// Render useful card content as readable text. Never execute card actions or
// fetch card-provided URLs; authenticated input and submit actions stay in Teams.
func renderCard(raw string) string {
	if len(raw) > 1<<20 {
		return "[Card too large — open Teams]"
	}
	var card any
	if json.Unmarshal([]byte(raw), &card) != nil {
		return "[Card — open Teams]"
	}
	var lines []string
	nodes := 0
	var walk func(any, int)
	walk = func(v any, depth int) {
		nodes++
		if depth > 20 || nodes > 1000 {
			return
		}
		switch x := v.(type) {
		case []any:
			for _, item := range x {
				walk(item, depth+1)
			}
		case map[string]any:
			typ, _ := x["type"].(string)
			if strings.HasPrefix(typ, "Input.") {
				lines = append(lines, "[Input — open Teams]")
				return
			}
			if strings.HasPrefix(typ, "Action.") {
				title, _ := x["title"].(string)
				if typ == "Action.OpenUrl" {
					u, _ := x["url"].(string)
					if link := safeLink(u); link != "" {
						lines = append(lines, title+": "+link)
					}
				} else {
					lines = append(lines, title+" [Action — open Teams]")
				}
				return
			}
			for _, key := range []string{"title", "subtitle", "text", "value"} {
				if s, ok := x[key].(string); ok && strings.TrimSpace(s) != "" {
					lines = append(lines, s)
				}
			}
			// Hero/thumbnail card buttons use a different schema.
			if typ == "openUrl" {
				if s, ok := x["value"].(string); ok && safeLink(s) != "" {
					lines = append(lines, s)
				}
			}
			for _, key := range []string{"body", "items", "columns", "facts", "actions", "buttons", "inlines"} {
				if child, ok := x[key]; ok {
					walk(child, depth+1)
				}
			}
		}
	}
	walk(card, 0)
	if len(lines) == 0 {
		return "[Rich card — open Teams]"
	}
	return strings.Join(lines, "\n")
}
