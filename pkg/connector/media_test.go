package connector

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
	"teamsbridge.local/teamsbridge/internal/graph"
)

type mediaIntent struct {
	bridgev2.MatrixAPI
	uploaded int
}

func (m *mediaIntent) UploadMedia(ctx context.Context, room id.RoomID, data []byte, name, mime string) (id.ContentURIString, *event.EncryptedFileInfo, error) {
	m.uploaded++
	return "", &event.EncryptedFileInfo{URL: "mxc://test/encrypted"}, nil
}
func TestHostedImageConversion(t *testing.T) {
	var data bytes.Buffer
	png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 12, 7)))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test" {
			t.Error("missing auth")
		}
		switch r.URL.Path {
		case "/v1.0/chats/chat/messages/msg/hostedContents":
			w.Write([]byte(`{"value":[{"id":"image"}]}`))
		case "/v1.0/chats/chat/messages/msg/hostedContents/image/$value":
			w.Write(data.Bytes())
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	c := &Client{api: &graph.Client{Base: server.URL + "/v1.0", HTTP: server.Client(), Token: func(context.Context) (string, error) { return "test", nil }}}
	m := graph.Message{ID: "msg"}
	m.Body.ContentType = "html"
	m.Body.Content = `<p>Screenshot</p><img src="../hostedContents/image/$value">`
	intent := &mediaIntent{}
	p := &bridgev2.Portal{Portal: &database.Portal{PortalKey: networkid.PortalKey{ID: "chat"}, MXID: "!room:test"}}
	got, err := c.convertMessage(context.Background(), p, intent, m)
	if err != nil {
		t.Fatal(err)
	}
	if intent.uploaded != 1 || len(got.Parts) != 1 {
		t.Fatalf("wrong parts/uploads: %d/%d", len(got.Parts), intent.uploaded)
	}
	pic := got.Parts[0].Content
	if pic.Body != "Screenshot" {
		t.Fatalf("missing caption: %q", pic.Body)
	}
	if pic.MsgType != event.MsgImage || pic.Info.Width != 12 || pic.Info.Height != 7 || pic.File == nil || pic.File.URL != "mxc://test/encrypted" || pic.URL != "" {
		t.Fatalf("incorrect encrypted image: %+v", pic)
	}
}

func TestUnsupportedHostedMediaDoesNotBlockHistory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/hostedContents") {
			w.Write([]byte(`{"value":[{"id":"unsupported"}]}`))
		} else {
			w.Write([]byte("not an image"))
		}
	}))
	defer server.Close()
	c := &Client{api: &graph.Client{Base: server.URL + "/v1.0", HTTP: server.Client(), Token: func(context.Context) (string, error) { return "test", nil }}}
	m := graph.Message{ID: "m"}
	m.Body.ContentType = "html"
	m.Body.Content = `<img src="../hostedContents/unsupported/$value">`
	p := &bridgev2.Portal{Portal: &database.Portal{PortalKey: networkid.PortalKey{ID: "chat"}}}
	out, err := c.convertMessage(context.Background(), p, &mediaIntent{}, m)
	if err != nil || len(out.Parts) != 1 || !strings.Contains(out.Parts[0].Content.Body, "open Teams") {
		t.Fatal(out, err)
	}
	if skippableMediaError(&graph.APIError{Status: 429}) {
		t.Fatal("throttled media must retry")
	}
}

func TestInterleavedImageCaptionsFollowHTMLOrder(t *testing.T) {
	c := &Client{}
	p := &bridgev2.Portal{Portal: &database.Portal{PortalKey: networkid.PortalKey{ID: "chat"}}}
	m := graph.Message{ID: "msg"}
	m.Body.ContentType = "html"
	m.Body.Content = `<p>First explanation</p><img src="../hostedContents/b/$value"><p>Second explanation</p><img src="../hostedContents/a/$value"><p>Closing text</p>`
	a := &bridgev2.ConvertedMessagePart{ID: "image_0", Content: &event.MessageEventContent{MsgType: event.MsgImage, Body: "a.png", FileName: "a.png", File: &event.EncryptedFileInfo{URL: "mxc://test/a"}}}
	b := &bridgev2.ConvertedMessagePart{ID: "image_1", Content: &event.MessageEventContent{MsgType: event.MsgImage, Body: "b.png", FileName: "b.png", File: &event.EncryptedFileInfo{URL: "mxc://test/b"}}}
	out := c.convertChatMessage(p, m)
	out.Parts = append(out.Parts, a, b)
	c.captionImages(p, m, out, map[string]*bridgev2.ConvertedMessagePart{"a": a, "b": b})
	if len(out.Parts) != 2 || out.Parts[0].ID != "" || out.Parts[0].Content.File.URL != "mxc://test/b" || out.Parts[0].Content.Body != "First explanation" {
		t.Fatalf("first caption/order: %+v", out.Parts)
	}
	if out.Parts[1].ID != "image_0" || !strings.Contains(out.Parts[1].Content.Body, "Second explanation") || !strings.Contains(out.Parts[1].Content.Body, "Closing text") {
		t.Fatalf("second caption: %+v", out.Parts[1])
	}
}
