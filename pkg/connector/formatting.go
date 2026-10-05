package connector

import (
	"html"
	"net/url"
	"strings"

	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	"maunium.net/go/mautrix/event"
)

// Keep Matrix-supported structure without forwarding Teams styles, executable
// content or remote images (the media pipeline handles those separately).
func matrixHTML(source string) string {
	nodes, err := xhtml.ParseFragment(strings.NewReader(source), &xhtml.Node{Type: xhtml.ElementNode, Data: "div", DataAtom: atom.Div})
	if err != nil {
		return html.EscapeString(source)
	}
	var out strings.Builder
	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		if n.Type == xhtml.TextNode {
			out.WriteString(html.EscapeString(n.Data))
			return
		}
		if n.Type != xhtml.ElementNode {
			return
		}
		tag := n.Data
		switch tag {
		case "script", "style", "iframe", "object", "embed", "img", "attachment":
			return
		}
		allowed := false
		switch tag {
		case "p", "br", "b", "strong", "i", "em", "u", "s", "del", "blockquote", "pre", "code", "ul", "ol", "li", "h1", "h2", "h3", "h4", "h5", "h6", "hr", "table", "thead", "tbody", "tr", "th", "td", "a", "sup", "sub":
			allowed = true
		case "div":
			tag = "p"
			allowed = true
		}
		if allowed {
			out.WriteString("<" + tag)
			if tag == "a" {
				for _, a := range n.Attr {
					if a.Key != "href" {
						continue
					}
					u, err := url.Parse(a.Val)
					if err == nil && u.User == nil && ((u.Scheme == "https" || u.Scheme == "http") && u.Host != "" || u.Scheme == "mailto") {
						out.WriteString(` href="` + html.EscapeString(u.String()) + `"`)
					}
				}
			}
			out.WriteString(">")
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
		if allowed && tag != "br" && tag != "hr" {
			out.WriteString("</" + tag + ">")
		}
	}
	for _, n := range nodes {
		walk(n)
	}
	return out.String()
}

func plainHTML(text string) string { return strings.ReplaceAll(html.EscapeString(text), "\n", "<br>") }

func appendPlain(content *event.MessageEventContent, text string) {
	content.Body += text
	if content.Format == event.FormatHTML {
		content.FormattedBody += plainHTML(text)
	}
}
