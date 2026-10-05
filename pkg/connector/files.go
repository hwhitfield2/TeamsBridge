package connector

import (
	"context"
	"encoding/base64"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"crypto/rand"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"teamsbridge.local/teamsbridge/internal/graph"
)

const maxFileSize = 20_000_000

type driveItem struct {
	WebDavURL   string `json:"webDavUrl"`
	ETag        string `json:"eTag"`
	ID          string `json:"id"`
	Name        string `json:"name"`
	WebURL      string `json:"webUrl"`
	Size        int64  `json:"size"`
	DownloadURL string `json:"@microsoft.graph.downloadUrl"`
	File        *struct {
		MIME string `json:"mimeType"`
	} `json:"file"`
	Parent struct {
		DriveID string `json:"driveId"`
	} `json:"parentReference"`
}

func safeDownloadURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, suffix := range []string{".sharepoint.com", ".1drv.com", ".onedrive.com"} {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}
func (c *Client) downloadFile(ctx context.Context, raw string) (driveItem, []byte, error) {
	var item driveItem
	if safeLink(raw) == "" {
		return item, nil, fmt.Errorf("invalid Teams attachment link")
	}
	share := "u!" + base64.RawURLEncoding.EncodeToString([]byte(raw))
	if err := c.api.Do(ctx, "GET", "/shares/"+share+"/driveItem", nil, &item); err != nil {
		return item, nil, err
	}
	if item.File == nil || item.Size < 0 || item.Size > maxFileSize || !safeDownloadURL(item.DownloadURL) {
		return item, nil, fmt.Errorf("file is too large or unavailable for direct download")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", item.DownloadURL, nil)
	if err != nil {
		return item, nil, err
	}
	// Preauthenticated URL returned by Graph. Never forward the Graph bearer token.
	hc := graph.HTTPClient()
	if c.fileHTTP != nil {
		copyClient := *c.fileHTTP
		copyClient.CheckRedirect = hc.CheckRedirect
		hc = &copyClient
	}
	response, err := hc.Do(req)
	if err != nil {
		return item, nil, fmt.Errorf("Teams file download failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return item, nil, fmt.Errorf("Teams file download returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxFileSize+1))
	if err != nil {
		return item, nil, err
	}
	if len(data) > maxFileSize {
		return item, nil, fmt.Errorf("Teams file exceeds 20 MB")
	}
	return item, data, nil
}
func (c *Client) appendFiles(ctx context.Context, p *bridgev2.Portal, intent bridgev2.MatrixAPI, m graph.Message, out *bridgev2.ConvertedMessage) error {
	if !c.hasScope("Files.ReadWrite") && !c.hasScope("Files.ReadWrite.All") {
		return nil
	}
	for i, a := range m.Attachments {
		if a.ContentType != "reference" || a.ContentURL == "" {
			continue
		}
		item, data, err := c.downloadFile(ctx, a.ContentURL)
		if err != nil {
			out.Parts[0].Content.Body += "\n[File download unavailable; use the attachment link]"
			continue
		}
		name := filepath.Base(item.Name)
		if name == "" || name == "." {
			name = "teams-file"
		}
		mime := item.File.MIME
		if mime == "" {
			mime = http.DetectContentType(data)
		}
		mxc, file, err := intent.UploadMedia(ctx, p.MXID, data, name, mime)
		if err != nil {
			return err
		}
		content := &event.MessageEventContent{MsgType: event.MsgFile, Body: name, FileName: name, URL: mxc, File: file, Info: &event.FileInfo{MimeType: mime, Size: len(data)}}
		if file != nil {
			content.URL = ""
		}
		out.Parts = append(out.Parts, &bridgev2.ConvertedMessagePart{ID: networkid.PartID(fmt.Sprintf("file_%d", i)), Type: event.EventMessage, Content: content})
	}
	return nil
}
func (c *Client) outgoingFile(ctx context.Context, msg *bridgev2.MatrixMessage) (any, string, error) {
	if err := c.requireWrite("Files.ReadWrite", "Files.ReadWrite.All"); err != nil {
		return nil, "", err
	}
	if msg.Content.Info != nil && msg.Content.Info.Size > maxFileSize {
		return nil, "", fmt.Errorf("file exceeds the bridge's 20 MB limit")
	}
	// Validate the reply and mentions before creating any remote file.
	content := *msg.Content
	content.Body = content.GetCaption()
	content.MsgType = event.MsgText
	proxy := *msg
	proxy.Content = &content
	path, payload, err := outgoingChatMessage(&proxy)
	if err != nil {
		return nil, "", err
	}
	if err = c.addOutgoingMentions(ctx, &proxy, payload); err != nil {
		return nil, "", err
	}
	name := msg.Content.FileName
	if name == "" {
		name = msg.Content.Body
	}
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`<>:"/\\|?*`, r) || r < 32 {
			return '_'
		}
		return r
	}, name)
	if name == "" || name == "." || name == ".." {
		name = "attachment"
	}
	if len(name) > 180 {
		return nil, "", fmt.Errorf("file name is too long")
	}
	var data []byte
	err = c.main.Bridge.Bot.DownloadMediaToFile(ctx, msg.Content.URL, msg.Content.File, false, func(f *os.File) error { var e error; data, e = io.ReadAll(io.LimitReader(f, maxFileSize+1)); return e })
	if err != nil {
		return nil, "", err
	}
	if len(data) > maxFileSize {
		return nil, "", fmt.Errorf("file exceeds 20 MB")
	}
	fileName := "TeamsBridge-" + newFileID() + "-" + name
	uploadPath := "/me/drive/root:/" + url.PathEscape(fileName) + ":/content"
	var recipients []map[string]string
	if channel, ok := channelPath(string(msg.Portal.ID)); ok {
		if err = c.requireWrite("Files.ReadWrite.All"); err != nil {
			return nil, "", err
		}
		var folder driveItem
		if err = c.api.Do(ctx, "GET", channel+"/filesFolder", nil, &folder); err != nil {
			return nil, "", err
		}
		if folder.ID == "" || folder.Parent.DriveID == "" {
			return nil, "", fmt.Errorf("channel has no accessible files folder")
		}
		uploadPath = "/drives/" + url.PathEscape(folder.Parent.DriveID) + "/items/" + url.PathEscape(folder.ID) + ":/" + url.PathEscape(fileName) + ":/content"
	} else {
		members, e := graph.List[graph.Member](ctx, c.api, graph.ChatPath(string(msg.Portal.ID))+"/members")
		if e != nil {
			return nil, "", e
		}
		if len(members) == 0 {
			return nil, "", fmt.Errorf("cannot determine file recipients for this chat")
		}
		seen := map[string]bool{}
		for _, member := range members {
			if member.UserID == "" {
				return nil, "", fmt.Errorf("cannot grant file access to an unidentified chat participant")
			}
			if member.UserID != c.meta.UserID && !seen[member.UserID] {
				recipients = append(recipients, map[string]string{"objectId": member.UserID})
				seen[member.UserID] = true
			}
		}
	}
	var item driveItem
	if err = c.api.Do(ctx, "PUT", uploadPath, graph.RawBody(data), &item); err != nil {
		return nil, "", err
	}
	if item.ID == "" || item.Parent.DriveID == "" || safeLink(item.WebURL) == "" {
		return nil, "", fmt.Errorf("file uploaded but Teams returned incomplete metadata; check OneDrive before retrying")
	}
	if len(recipients) > 0 {
		invite := map[string]any{"recipients": recipients, "roles": []string{"read"}, "requireSignIn": true, "sendInvitation": false}
		var result struct {
			Value []struct {
				Error jsonError `json:"error"`
			} `json:"value"`
		}
		err = c.api.Do(ctx, "POST", "/drives/"+url.PathEscape(item.Parent.DriveID)+"/items/"+url.PathEscape(item.ID)+"/invite", invite, &result)
		if err == nil && len(result.Value) == 0 {
			err = fmt.Errorf("no sharing permissions returned")
		}
		if err == nil {
			for _, r := range result.Value {
				if r.Error.Code != "" {
					err = fmt.Errorf("partial sharing failure")
					break
				}
			}
		}
		if err != nil {
			return nil, "", fmt.Errorf("file uploaded to OneDrive but recipient access failed; no message sent: %w", err)
		}
	}
	var reference driveItem
	if err = c.api.Do(ctx, "GET", "/drives/"+url.PathEscape(item.Parent.DriveID)+"/items/"+url.PathEscape(item.ID)+"?$select=id,name,webDavUrl,eTag", nil, &reference); err != nil {
		return nil, "", fmt.Errorf("file uploaded but attachment metadata failed; check OneDrive before retrying: %w", err)
	}
	attachmentID := strings.Trim(strings.SplitN(reference.ETag, ",", 2)[0], "\"{}")
	if len(attachmentID) != 36 || strings.Trim(attachmentID, "0123456789abcdefABCDEF-") != "" || safeLink(reference.WebDavURL) == "" {
		return nil, "", fmt.Errorf("file uploaded but attachment metadata is incomplete; check OneDrive before retrying")
	}
	message := payload.(map[string]any)
	if reply, ok := message["replyMessage"]; ok {
		message = reply.(map[string]any)
	}
	body := message["body"].(map[string]string)
	if body["contentType"] != "html" {
		body["content"] = strings.ReplaceAll(html.EscapeString(body["content"]), "\n", "<br>")
	}
	body["contentType"] = "html"
	body["content"] += `<attachment id="` + attachmentID + `"></attachment>`
	message["attachments"] = []map[string]string{{"id": attachmentID, "contentType": "reference", "contentUrl": reference.WebDavURL, "name": name}}
	return payload, path, nil
}

type jsonError struct {
	Code string `json:"code"`
}

func isInlineMedia(content *event.MessageEventContent) bool {
	if content.MsgType == event.MsgImage {
		return true
	}
	if content.Info == nil {
		return false
	}
	return content.Info.MimeType == "image/gif" || (content.MsgType == event.MsgVideo && content.Info.MauGIF)
}

func newFileID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
