package connector

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/gif"
	"strings"
	"testing"
)

func TestHostedGIFPreservesAnimationAndReply(t *testing.T) {
	pal := color.Palette{color.Black, color.White}
	a := image.NewPaletted(image.Rect(0, 0, 2, 2), pal)
	b := image.NewPaletted(image.Rect(0, 0, 2, 2), pal)
	b.SetColorIndex(0, 0, 1)
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, &gif.GIF{Image: []*image.Paletted{a, b}, Delay: []int{10, 10}, LoopCount: 0}); err != nil {
		t.Fatal(err)
	}
	for _, reply := range []bool{false, true} {
		message := map[string]any{"body": map[string]string{"contentType": "text", "content": "A & B"}}
		payload := message
		if reply {
			payload = map[string]any{"messageIds": []string{"root"}, "replyMessage": message}
		}
		if err := addHostedImage(payload, buf.Bytes()); err != nil {
			t.Fatal(err)
		}
		hosted := message["hostedContents"].([]map[string]string)[0]
		got, err := base64.StdEncoding.DecodeString(hosted["contentBytes"])
		if err != nil || !bytes.Equal(got, buf.Bytes()) || hosted["contentType"] != "image/gif" {
			t.Fatal("animation bytes changed")
		}
		if !strings.Contains(message["body"].(map[string]string)["content"], `A &amp; B<img src="../hostedContents/1/$value"`) {
			t.Fatal("incorrect caption or reference")
		}
	}
}
func TestHostedMediaRejectsInvalidAndOversize(t *testing.T) {
	for _, data := range [][]byte{nil, []byte("not an image"), make([]byte, maxHostedImage+1)} {
		if addHostedImage(nil, data) == nil {
			t.Fatal("invalid media accepted")
		}
	}
}
