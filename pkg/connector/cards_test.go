package connector

import (
	"encoding/json"
	"strings"
	"testing"

	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"
	"teamsbridge.local/teamsbridge/internal/graph"
)

func TestAdaptiveCardOnlyMessage(t *testing.T) {
	raw := `{"body":{"contentType":"html","content":"<attachment id=\"card\"></attachment>"},"attachments":[{"contentType":"application/vnd.microsoft.card.adaptive","content":""}]}`
	var m graph.Message
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	m.Attachments[0].Content = `{"type":"AdaptiveCard","body":[{"type":"TextBlock","weight":"bolder","text":"Report title"},{"type":"FactSet","facts":[{"title":"Status","value":"**Ready**"}]},{"type":"TextBlock","text":"3. Review [details](https://example.com)"},{"type":"Container","items":[{"type":"RichTextBlock","inlines":[{"type":"TextRun","fontType":"monospace","text":"first <line>"}]},{"type":"RichTextBlock","inlines":[{"type":"TextRun","fontType":"monospace","text":"  second line"}]}]}]}`
	got := convert(m).Parts[0].Content
	if got.Format != event.FormatHTML {
		t.Fatal("card-only message lost HTML")
	}
	for _, want := range []string{"<strong>Report title</strong>", "<strong>Status:</strong>", "<strong>Ready</strong>", `start="3"`, `href="https://example.com"`, "<pre><code>first &lt;line&gt;\n  second line</code></pre>"} {
		if !strings.Contains(got.FormattedBody, want) {
			t.Fatalf("missing %s: %s", want, got.FormattedBody)
		}
	}
	parts := []*database.Message{{PartID: "", Metadata: &MessageMetadata{RenderingRevision: 7}}}
	if !needsBotHTMLRepair(parts, m) {
		t.Fatal("old card not repaired")
	}
	parts[0].Metadata = &MessageMetadata{RenderingRevision: 8}
	if needsBotHTMLRepair(parts, m) {
		t.Fatal("card repeatedly repaired")
	}
	parts[0].PartID = "media"
	if needsBotHTMLRepair(parts, m) {
		t.Fatal("media layout selected for repair")
	}
}

func TestCardSafetyAndLimits(t *testing.T) {
	got := renderCardHTML(`{"body":[{"type":"TextBlock","text":"<img src=x onerror=bad()> [bad](javascript:bad)"},{"type":"Action.OpenUrl","url":"javascript:bad"},{"type":"TextRun","text":"<script>bad()</script>"},{"type":"Input.Text"}]}`)
	for _, bad := range []string{"<img", "<script", `href="javascript:`} {
		if strings.Contains(got, bad) {
			t.Fatal(got)
		}
	}
	if !strings.Contains(got, "open Teams") {
		t.Fatal("missing input fallback")
	}
	if !strings.Contains(renderCardHTML(strings.Repeat("x", (1<<20)+1)), "too large") {
		t.Fatal("size limit")
	}
	if !strings.Contains(renderCardHTML("{"), "open Teams") {
		t.Fatal("malformed fallback")
	}
	nested := `{"type":"TextBlock","text":"deep"}`
	for i := 0; i < 30; i++ {
		nested = `{"items":[` + nested + `]}`
	}
	if strings.Contains(renderCardHTML(nested), "deep") {
		t.Fatal("depth limit")
	}
}
