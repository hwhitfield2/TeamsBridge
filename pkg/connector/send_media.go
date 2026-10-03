package connector

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"html"
	"image"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
)

const maxHostedImage = 4_000_000

func addHostedImage(payload any, data []byte) error {
	if len(data) == 0 || len(data) > maxHostedImage {
		return fmt.Errorf("Teams inline images and GIFs must be under 4 MB")
	}
	mime := http.DetectContentType(data)
	if mime != "image/gif" && mime != "image/png" && mime != "image/jpeg" {
		return fmt.Errorf("unsupported image format: %s", mime)
	}
	dimensions, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("invalid image: %w", err)
	}
	message := payload.(map[string]any)
	if reply, ok := message["replyMessage"]; ok {
		message = reply.(map[string]any)
	}
	body := message["body"].(map[string]string)
	caption := body["content"]
	if body["contentType"] != "html" {
		caption = strings.ReplaceAll(html.EscapeString(caption), "\n", "<br>")
	}
	body["contentType"] = "html"
	body["content"] = caption + fmt.Sprintf(`<img src="../hostedContents/1/$value" width="%d" height="%d">`, dimensions.Width, dimensions.Height)
	message["hostedContents"] = []map[string]string{{"@microsoft.graph.temporaryId": "1", "contentType": mime, "contentBytes": base64.StdEncoding.EncodeToString(data)}}
	return nil
}
func (c *Client) outgoingMedia(ctx context.Context, msg *bridgev2.MatrixMessage) (any, string, error) {
	content := *msg.Content
	if content.Info != nil && content.Info.Size > 20_000_000 {
		return nil, "", fmt.Errorf("GIF source exceeds 20 MB")
	}
	var data []byte
	err := c.main.Bridge.Bot.DownloadMediaToFile(ctx, content.URL, content.File, false, func(f *os.File) error {
		var err error
		data, err = io.ReadAll(io.LimitReader(f, 20_000_001))
		return err
	})
	if err != nil {
		return nil, "", fmt.Errorf("download GIF/image from Beeper: %w", err)
	}
	if len(data) > 20_000_000 {
		return nil, "", fmt.Errorf("GIF source exceeds 20 MB")
	}
	if content.MsgType == event.MsgVideo {
		if content.Info == nil || !content.Info.MauGIF {
			return nil, "", bridgev2.ErrUnsupportedMessageType
		}
		data, err = videoGIF(ctx, data)
		if err != nil {
			return nil, "", err
		}
	}
	content.Body = content.GetCaption()
	content.MsgType = event.MsgText
	copyMsg := *msg
	copyMsg.Content = &content
	path, payload, err := outgoingChatMessage(&copyMsg)
	if err != nil {
		return nil, "", err
	}
	if err = c.addOutgoingMentions(ctx, &copyMsg, payload); err != nil {
		return nil, "", err
	}
	if err = addHostedImage(payload, data); err != nil {
		return nil, "", err
	}
	return payload, path, nil
}
func videoGIF(ctx context.Context, data []byte) ([]byte, error) {
	binary, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, fmt.Errorf("video-format GIFs require ffmpeg; attach a .gif file instead")
	}
	dir, err := os.MkdirTemp("", "teams-gif-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	source := filepath.Join(dir, "input.mp4")
	dest := filepath.Join(dir, "output.gif")
	if err = os.WriteFile(source, data, 0600); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// Restrict input protocols to local files and bound CPU time and output size.
	cmd := exec.CommandContext(ctx, binary, "-nostdin", "-v", "error", "-protocol_whitelist", "file", "-i", source, "-an", "-vf", "fps=12,scale=480:-1:force_original_aspect_ratio=decrease", "-fs", "4000001", dest)
	if err = cmd.Run(); err != nil {
		return nil, fmt.Errorf("could not convert video GIF; attach a .gif file instead")
	}
	output, err := os.ReadFile(dest)
	if err != nil {
		return nil, err
	}
	if len(output) > maxHostedImage {
		return nil, fmt.Errorf("converted GIF exceeds Teams' 4 MB limit")
	}
	return output, nil
}
