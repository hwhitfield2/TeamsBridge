package connector

import (
	"bytes"
	"context"
	"errors"
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
	defer func() {
		if len(out.Parts) > 1 && out.Parts[0].Content.Body == "[Teams message: open Teams to view this content]" {
			out.Parts = out.Parts[1:]
			out.Parts[0].ID = ""
		}
		stampMessage(out, m)
	}()
	if err := c.appendGIFs(ctx, p, intent, m, out); err != nil {
		return nil, err
	}
	if err := c.appendFiles(ctx, p, intent, m, out); err != nil {
		return nil, err
	}
	refs := hostedImageIDs(m)
	if len(refs) == 0 {
		return out, nil
	}
	path := messageResource(string(p.ID), m) + "/hostedContents"
	hosted, err := graph.List[struct {
		ID string `json:"id"`
	}](ctx, c.api, path)
	if err != nil {
		return nil, fmt.Errorf("list Teams images: %w", err)
	}
	for i, item := range hosted {
		if item.ID == "" || !refs[item.ID] {
			continue
		}
		var data []byte
		if err = c.api.Do(ctx, "GET", path+"/"+url.PathEscape(item.ID)+"/$value", nil, &data); err != nil {
			if skippableMediaError(err) {
				out.Parts[0].Content.Body += "\n[Teams media unavailable or unsupported — open Teams to view it]"
				continue
			}
			return nil, fmt.Errorf("download Teams image: %w", err)
		}
		dimensions, kind, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			out.Parts[0].Content.Body += "\n[Teams image could not be decoded — open Teams to view it]"
			continue
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

func skippableMediaError(err error) bool {
	var apiErr *graph.APIError
	return errors.Is(err, graph.ErrUnsupportedImage) || errors.Is(err, graph.ErrImageTooLarge) || (errors.As(err, &apiErr) && (apiErr.Status == 403 || apiErr.Status == 404))
}

func hostedImageIDs(m graph.Message) map[string]bool {
	refs := map[string]bool{}
	z := html.NewTokenizer(strings.NewReader(m.Body.Content))
	for {
		typ := z.Next()
		if typ == html.ErrorToken {
			break
		}
		if typ != html.StartTagToken && typ != html.SelfClosingTagToken {
			continue
		}
		tok := z.Token()
		if tok.Data != "img" {
			continue
		}
		for _, a := range tok.Attr {
			if a.Key != "src" {
				continue
			}
			u, err := url.Parse(a.Val)
			if err != nil {
				continue
			}
			if u.IsAbs() && (u.Scheme != "https" || u.Host != "graph.microsoft.com") {
				continue
			}
			parts := strings.SplitN(u.Path, "/hostedContents/", 2)
			if len(parts) != 2 {
				continue
			}
			id := strings.TrimSuffix(parts[1], "/$value")
			if id != "" && !strings.Contains(id, "/") {
				refs[id] = true
			}
		}
	}
	return refs
}
