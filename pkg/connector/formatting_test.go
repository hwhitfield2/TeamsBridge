package connector

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"
	"teamsbridge.local/teamsbridge/internal/graph"
)

func TestFacilitatorHTML(t *testing.T) {
	m := graph.Message{ID: "1"}
	m.Body.ContentType = "html"
	m.Body.Content = `<p>About five minutes remain; please <b>close open questions and owners</b>:</p><ul><li>Confirm owners. <cite id="1">[1]</cite></li></ul><b>OKR targets</b><ul><li><b>Performance:</b> Review targets.</li></ul>`
	got := convert(m).Parts[0].Content
	for _, want := range []string{"<b>close open questions and owners</b>", "<ul><li>Confirm owners. [1]</li></ul>", "<b>Performance:</b>"} {
		if got.Format != event.FormatHTML || !strings.Contains(got.FormattedBody, want) {
			t.Fatalf("missing %s in %#v", want, got)
		}
	}
	applyMentions(got, []inboundMention{{marker: "owners", label: "Owners"}})
	if !strings.Contains(got.FormattedBody, "<ul>") || !strings.Contains(got.FormattedBody, "@Owners") {
		t.Fatal(got.FormattedBody)
	}
	appendPlain(got, "\n<unavailable>")
	if !strings.HasSuffix(got.FormattedBody, "<br>&lt;unavailable&gt;") {
		t.Fatal(got.FormattedBody)
	}
}

func TestHTMLBoundariesAndPlainText(t *testing.T) {
	got := matrixHTML(`<p onclick="bad()">Hello <script>bad()</script><img src="https://evil.test/x"><a href="javascript:bad()">bad</a><a href="https://example.com/?a=1&amp;b=2">good</a><cite>[2]</cite></p>`)
	for _, bad := range []string{"onclick", "script", "img", "evil.test", "javascript:"} {
		if strings.Contains(got, bad) {
			t.Fatal(got)
		}
	}
	if !strings.Contains(got, `href="https://example.com/?a=1&amp;b=2"`) || !strings.Contains(got, "[2]") {
		t.Fatal(got)
	}
	m := graph.Message{}
	m.Body.Content = `literal **text** and <b>tags</b>`
	gotPlain := convert(m).Parts[0].Content
	if gotPlain.Format != "" || gotPlain.Body != m.Body.Content {
		t.Fatal(gotPlain)
	}
}

func TestBotHTMLRepairKeepsExistingPart(t *testing.T) {
	c := &Client{meta: &Metadata{UserID: "self"}}
	m := graph.Message{ID: "1"}
	m.From.Application = &graph.User{ID: "bot"}
	m = c.normalizeSender(m)
	m.Body.ContentType = "html"
	m.Body.Content = "<b>Actions</b><ul><li>Owner</li></ul>"
	part := &database.Message{ID: "chat/1", Metadata: &MessageMetadata{ContentHash: contentHash(m), PartCount: 1, RenderingRevision: 6}}
	result, err := c.handleExisting(context.Background(), replyPortal(), nil, []*database.Message{part}, m)
	if err != nil || len(result.SubEvents) != 1 {
		t.Fatalf("repair not scheduled: %#v %v", result, err)
	}
	edit, err := c.convertEdit(context.Background(), replyPortal(), nil, []*database.Message{part}, m)
	if err != nil || len(edit.ModifiedParts) != 1 || edit.AddedParts != nil || len(edit.DeletedParts) != 0 {
		t.Fatalf("unsafe edit: %#v %v", edit, err)
	}
	if edit.ModifiedParts[0].Content.Format != event.FormatHTML {
		t.Fatal("missing formatted edit")
	}
	result, err = c.handleExisting(context.Background(), replyPortal(), nil, []*database.Message{part}, m)
	if err != nil || len(result.SubEvents) != 0 {
		t.Fatal("repair repeated")
	}
}

func TestFormattedReplySubjectAndAttachment(t *testing.T) {
	c := &Client{meta: &Metadata{UserID: "self"}}
	m := graph.Message{ID: "2", Subject: "Review <today>"}
	m.Body.ContentType = "html"
	m.Body.Content = "<b>Answer</b>"
	if err := json.Unmarshal([]byte(`[{"contentType":"messageReference","content":"{\"messageId\":\"1\",\"messagePreview\":\"Prior <topic>\"}"},{"name":"report.txt","contentUrl":"https://example.com/report.txt"}]`), &m.Attachments); err != nil {
		t.Fatal(err)
	}
	got := c.convertChatMessage(replyPortal(), m).Parts[0].Content
	for _, want := range []string{"Review &lt;today&gt;", "<blockquote>Prior &lt;topic&gt;</blockquote>", "<b>Answer</b>", "[Attachment: report.txt] https://example.com/report.txt"} {
		if !strings.Contains(got.FormattedBody, want) {
			t.Fatalf("missing %s: %s", want, got.FormattedBody)
		}
	}
}
