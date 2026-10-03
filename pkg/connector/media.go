package connector

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/html"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"teamsbridge.local/teamsbridge/internal/graph"
)

func hasImages(m graph.Message) bool {
	if !strings.EqualFold(m.Body.ContentType, "html") {
		return false
	}
	z := html.NewTokenizer(strings.NewReader(m.Body.Content))
	for {
		switch z.Next() {
		case html.ErrorToken:
			return false
		case html.StartTagToken, html.SelfClosingTagToken:
			n, _ := z.TagName()
			if string(n) == "img" {
				return true
			}
		}
	}
}

// Resolve hosted content through this message's Graph resource, never through
// untrusted HTML URLs. UploadMedia handles encryption for the destination room.
func (c *Client) convertMessage(ctx context.Context, p *bridgev2.Portal, intent bridgev2.MatrixAPI, m graph.Message) (*bridgev2.ConvertedMessage, error) {
	out := c.convertChatMessage(p, m)
	if !hasImages(m) {
		return out, nil
	}
	path := graph.ChatPath(string(p.ID)) + "/messages/" + url.PathEscape(m.ID) + "/hostedContents"
	hosted, err := graph.List[struct {
		ID string `json:"id"`
	}](ctx, c.api, path)
	if err != nil {
		return nil, fmt.Errorf("list Teams images: %w", err)
	}
	for i, item := range hosted {
		if item.ID == "" {
			continue
		}
		var data []byte
		if err = c.api.Do(ctx, "GET", path+"/"+url.PathEscape(item.ID)+"/$value", nil, &data); err != nil {
			return nil, fmt.Errorf("download Teams image: %w", err)
		}
		dimensions, kind, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("decode Teams image: %w", err)
		}
		filename := fmt.Sprintf("teams-image-%d.%s", i+1, kind)
		mime := http.DetectContentType(data)
		mxc, file, err := intent.UploadMedia(ctx, p.MXID, data, filename, mime)
		if err != nil {
			return nil, fmt.Errorf("upload Teams image: %w", err)
		}
		content := &event.MessageEventContent{MsgType: event.MsgImage, Body: filename, FileName: filename, URL: mxc, File: file, Info: &event.FileInfo{MimeType: mime, Size: len(data), Width: dimensions.Width, Height: dimensions.Height}}
		if file != nil {
			content.URL = ""
		}
		out.Parts = append(out.Parts, &bridgev2.ConvertedMessagePart{ID: networkid.PartID(fmt.Sprintf("image_%d", i)), Type: event.EventMessage, Content: content})
	}
	if len(out.Parts) > 1 && out.Parts[0].Content.Body == "[Teams message: open Teams to view this content]" {
		out.Parts = out.Parts[1:]
		out.Parts[0].ID = ""
	}
	return out, nil
}
