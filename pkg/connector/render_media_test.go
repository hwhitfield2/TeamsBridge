package connector

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/gif"
	"io"
	"net/http"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"teamsbridge.local/teamsbridge/internal/graph"
)

func TestIncomingGIFPreservesFrames(t *testing.T) {
	pal := color.Palette{color.Black, color.White}
	a := image.NewPaletted(image.Rect(0, 0, 2, 2), pal)
	b := image.NewPaletted(image.Rect(0, 0, 2, 2), pal)
	b.SetColorIndex(0, 0, 1)
	var buf bytes.Buffer
	gif.EncodeAll(&buf, &gif.GIF{Image: []*image.Paletted{a, b}, Delay: []int{10, 10}})
	c := &Client{fileHTTP: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "" {
			t.Error("token leaked to Giphy")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(buf.Bytes())), Header: http.Header{}}, nil
	})}}
	data, err := c.downloadGIF(context.Background(), "https://media2.giphy.com/media/test/giphy.gif")
	if err != nil || !bytes.Equal(data, buf.Bytes()) {
		t.Fatal("GIF bytes changed", err)
	}
	decoded, err := gif.DecodeAll(bytes.NewReader(data))
	if err != nil || len(decoded.Image) != 2 {
		t.Fatal("animation lost")
	}
	for _, raw := range []string{"https://media2.giphy.com.evil.com/x", "http://media2.giphy.com/x", "https://localhost/gif", "https://user@media.giphy.com/x"} {
		if approvedGIFURL(raw) {
			t.Fatal(raw)
		}
	}
	m := graph.Message{}
	m.Body.Content = `<img src="https://media2.giphy.com/media/test/giphy.gif">`
	if len(externalGIFs(m)) != 1 || len(hostedImageIDs(m)) != 0 {
		t.Fatal("external GIF treated as hosted reaction content")
	}
}
func TestCustomReactionIdentityAndImage(t *testing.T) {
	p := &bridgev2.Portal{Portal: &database.Portal{PortalKey: networkid.PortalKey{ID: "chat"}}}
	c := &Client{api: &graph.Client{}, meta: &Metadata{Media: map[string]string{}}}
	m := graph.Message{ID: "msg"}
	r := graph.Reaction{Type: "custom", DisplayName: "party", ContentURL: "https://graph.microsoft.com/v1.0/chats/chat/messages/msg/hostedContents/emoji/$value"}
	r.User.User = &graph.User{ID: "user"}
	m.Reactions = []graph.Reaction{r}
	c.meta.Media[string(customReactionID(r))] = "mxc://test/emoji"
	got, err := c.reactionDataWithMedia(context.Background(), p, nil, m)
	if err != nil {
		t.Fatal(err)
	}
	reaction := got.Users["user"].Reactions[0]
	if reaction.Emoji != "mxc://test/emoji" || reaction.ExtraContent["com.beeper.reaction.shortcode"] != ":party:" {
		t.Fatal(reaction)
	}
	other := r
	other.ContentURL = "https://graph.microsoft.com/v1.0/chats/chat/messages/msg/hostedContents/other/$value"
	if customReactionID(other) == customReactionID(r) {
		t.Fatal("distinct custom reactions collapsed")
	}
	for _, raw := range []string{"https://evil.com/v1.0/chats/chat/messages/msg/hostedContents/emoji/$value", "https://graph.microsoft.com/v1.0/chats/other/messages/msg/hostedContents/emoji/$value"} {
		r.ContentURL = raw
		if _, err = c.customReactionImage(context.Background(), p, nil, m, r); err == nil {
			t.Fatal("untrusted custom reaction downloaded")
		}
	}
}

func TestUnsupportedCustomReactionDoesNotBlockMessages(t *testing.T) {
	c := &Client{meta: &Metadata{}, api: &graph.Client{Token: func(context.Context) (string, error) { return "test", nil }, HTTP: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewBufferString("unsupported artwork")), Header: http.Header{}}, nil
	})}}}
	p := &bridgev2.Portal{Portal: &database.Portal{PortalKey: networkid.PortalKey{ID: "chat"}}}
	r := graph.Reaction{Type: "custom", DisplayName: "party", ContentURL: "https://graph.microsoft.com/v1.0/chats/chat/messages/msg/hostedContents/emoji/$value"}
	r.User.User = &graph.User{ID: "user"}
	out, err := c.reactionDataWithMedia(context.Background(), p, nil, graph.Message{ID: "msg", Reactions: []graph.Reaction{r}})
	if err != nil || out.Users["user"].Reactions[0].Emoji != ":party:" {
		t.Fatal("unsupported custom reaction blocked message", err)
	}
}

func TestUnavailableGIFBecomesStableFallback(t *testing.T) {
	for _, status := range []int{403, 404, 410, 429, 503} {
		c := &Client{fileHTTP: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(nil)), Header: http.Header{}}, nil
		})}}
		m := graph.Message{}
		m.Body.ContentType = "html"
		m.Body.Content = `<p>Before</p><img src="https://media.giphy.com/missing.gif"><p>After</p>`
		p := &bridgev2.Portal{Portal: &database.Portal{PortalKey: networkid.PortalKey{ID: "chat"}}}
		out, err := c.convertMessage(context.Background(), p, nil, m)
		if status == 429 || status == 503 {
			if err == nil {
				t.Fatal("transient failure must retry", status)
			}
			continue
		}
		if err != nil || len(out.Parts) != 3 || out.Parts[0].Content.Body != "Before" || out.Parts[2].Content.Body != "After" {
			t.Fatal("permanent GIF error blocked message or lost order", status, err)
		}
	}
	c := &Client{fileHTTP: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(io.LimitReader(zeroReader{}, maxFileSize+1)), Header: http.Header{}}, nil
	})}}
	_, err := c.downloadGIF(context.Background(), "https://media.giphy.com/large.gif")
	if !skippableMediaError(err) {
		t.Fatal("oversized GIF must not retry", err)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }
