package connector

import (
	"encoding/json"
	"strings"
	"teamsbridge.local/teamsbridge/internal/graph"
	"testing"
)

func TestMalformedForwardAndUnnamedAttachment(t *testing.T) {
	var m graph.Message
	if err := json.Unmarshal([]byte(`{"body":{"contentType":"html","content":"<p>Comment</p><attachment id=\"f\"></attachment>"},"attachments":[{"id":"f","contentType":"forwardedMessageReference","content":"invalid"},{"id":"file","contentType":"reference"}]}`), &m); err != nil {
		t.Fatal(err)
	}
	out := convert(expandForwardedMessages(m)).Parts[0].Content.Body
	if !strings.Contains(out, "Comment") || !strings.Contains(out, "Forwarded Teams message") || strings.Contains(out, "[Attachment: ]") {
		t.Fatalf("bad fallback: %s", out)
	}
}

func TestForwardEscapedNewlinesAndMissingPlaceholder(t *testing.T) {
	var m graph.Message
	content, _ := json.Marshal(map[string]string{"originalMessageContent": `<p>First</p>\n<p>Second</p>`})
	payload, _ := json.Marshal(map[string]any{"body": map[string]string{"contentType": "html", "content": "<p>Comment</p>"}, "attachments": []any{map[string]string{"id": "f", "contentType": "forwardedMessageReference", "content": string(content)}}})
	json.Unmarshal(payload, &m)
	expanded := expandForwardedMessages(m)
	out := convert(expanded).Parts[0].Content.Body
	if strings.Contains(out, `\n`) || !strings.Contains(out, "First") || !strings.Contains(out, "Second") || len(expanded.Attachments) != 0 {
		t.Fatalf("bad expansion: %s", out)
	}
	if expandForwardedMessages(expanded).Body.Content != expanded.Body.Content {
		t.Fatal("expansion must be idempotent")
	}
}
