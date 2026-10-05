package connector

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"image"
	"io"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/html"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"teamsbridge.local/teamsbridge/internal/graph"
)

const renderingRevision = 4

func approvedGIFURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return h == "media.giphy.com" || h == "i.giphy.com" || h == "media0.giphy.com" || h == "media1.giphy.com" || h == "media2.giphy.com" || h == "media3.giphy.com" || h == "media4.giphy.com" || h == "media.tenor.com" || h == "c.tenor.com"
}
func externalGIFs(m graph.Message) []string {
	var sources []string
	seen := map[string]bool{}
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
			if a.Key == "src" && approvedGIFURL(a.Val) && !seen[a.Val] {
				seen[a.Val] = true
				sources = append(sources, a.Val)
			}
		}
	}
	return sources
}
func (c *Client) downloadGIF(ctx context.Context, raw string) ([]byte, error) {
	if !approvedGIFURL(raw) {
		return nil, fmt.Errorf("untrusted GIF URL")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", raw, nil)
	if err != nil {
		return nil, err
	}
	hc := graph.HTTPClient()
	if c.fileHTTP != nil {
		copy := *c.fileHTTP
		copy.CheckRedirect = hc.CheckRedirect
		hc = &copy
	}
	response, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GIF download failed")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("GIF download HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxFileSize {
		return nil, fmt.Errorf("GIF exceeds 20 MB")
	}
	if http.DetectContentType(data) != "image/gif" {
		return nil, fmt.Errorf("GIF provider returned an unsupported format")
	}
	return data, nil
}
func (c *Client) appendGIFs(ctx context.Context, p *bridgev2.Portal, intent bridgev2.MatrixAPI, m graph.Message, out *bridgev2.ConvertedMessage) error {
	for i, src := range externalGIFs(m) {
		data, err := c.downloadGIF(ctx, src)
		if err != nil {
			return err
		}
		dims, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return err
		}
		name := fmt.Sprintf("teams-animation-%d.gif", i+1)
		mxc, file, err := intent.UploadMedia(ctx, p.MXID, data, name, "image/gif")
		if err != nil {
			return err
		}
		content := &event.MessageEventContent{MsgType: event.MsgImage, Body: name, FileName: name, URL: mxc, File: file, Info: &event.FileInfo{MimeType: "image/gif", Size: len(data), Width: dims.Width, Height: dims.Height, MauGIF: true}}
		if file != nil {
			content.URL = ""
		}
		out.Parts = append(out.Parts, &bridgev2.ConvertedMessagePart{ID: networkid.PartID(fmt.Sprintf("gif_%d", i)), Type: event.EventMessage, Content: content})
	}
	return nil
}
func customReactionID(r graph.Reaction) networkid.EmojiID {
	if r.Type != "custom" {
		return networkid.EmojiID(reactionEmoji(r.Type))
	}
	hash := sha256.Sum256([]byte(r.ContentURL + "\x00" + r.DisplayName))
	return networkid.EmojiID(fmt.Sprintf("custom:%x", hash[:16]))
}
func (c *Client) customReactionImage(ctx context.Context, p *bridgev2.Portal, intent bridgev2.MatrixAPI, m graph.Message, r graph.Reaction) (string, error) {
	u, err := url.Parse(r.ContentURL)
	if err != nil {
		return "", fmt.Errorf("invalid custom reaction URL")
	}
	// Reaction content must belong to the message being rendered, on Graph v1.0.
	base := c.api.Base
	if base == "" {
		base = graph.BaseURL
	}
	b, _ := url.Parse(base)
	prefix := b.Path + messageResource(string(p.ID), m) + "/hostedContents/"
	decodedPrefix, _ := url.PathUnescape(prefix)
	if u.Scheme != b.Scheme || u.Host != b.Host || u.User != nil || u.RawQuery != "" || !strings.HasPrefix(u.Path, decodedPrefix) || !strings.HasSuffix(u.Path, "/$value") {
		return "", fmt.Errorf("custom reaction URL is outside this message")
	}
	hostedID := strings.TrimSuffix(strings.TrimPrefix(u.Path, decodedPrefix), "/$value")
	if hostedID == "" || strings.Contains(hostedID, "/") {
		return "", fmt.Errorf("invalid custom reaction hosted content ID")
	}
	c.mediaMu.Lock()
	defer c.mediaMu.Unlock()
	key := string(customReactionID(r))
	c.meta.mu.Lock()
	cached := c.meta.Media[key]
	c.meta.mu.Unlock()
	if cached != "" {
		return cached, nil
	}
	var data []byte
	if err = c.api.Do(ctx, "GET", r.ContentURL, nil, &data); err != nil {
		return "", err
	}
	_, kind, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	// Matrix reaction keys require an unencrypted MXC asset, like custom emoji.
	mxc, file, err := intent.UploadMedia(ctx, "", data, "reaction."+kind, http.DetectContentType(data))
	if err != nil {
		return "", err
	}
	if file != nil || mxc == "" {
		return "", fmt.Errorf("custom reaction upload did not return an MXC URL")
	}
	c.authMu.Lock()
	defer c.authMu.Unlock()
	c.meta.mu.Lock()
	if c.meta.Media == nil {
		c.meta.Media = map[string]string{}
	}
	if len(c.meta.Media) >= 1024 {
		for old := range c.meta.Media {
			delete(c.meta.Media, old)
			break
		}
	}
	c.meta.Media[key] = string(mxc)
	c.meta.mu.Unlock()
	if err = c.login.Save(ctx); err != nil {
		return "", err
	}
	return string(mxc), nil
}
func (c *Client) reactionDataWithMedia(ctx context.Context, p *bridgev2.Portal, intent bridgev2.MatrixAPI, m graph.Message) (*bridgev2.ReactionSyncData, error) {
	out := c.reactionData(m)
	for _, r := range m.Reactions {
		if r.Type != "custom" || r.User.User == nil || r.User.User.ID == "" || r.ContentURL == "" {
			continue
		}
		mxc, err := c.customReactionImage(ctx, p, intent, m, r)
		if err != nil {
			return nil, err
		}
		key := customReactionID(r)
		for _, reaction := range out.Users[networkid.UserID(r.User.User.ID)].Reactions {
			if reaction.EmojiID == key {
				reaction.Emoji = mxc
				reaction.ExtraContent = map[string]any{"com.beeper.reaction.shortcode": ":" + r.DisplayName + ":"}
			}
		}
	}
	return out, nil
}
