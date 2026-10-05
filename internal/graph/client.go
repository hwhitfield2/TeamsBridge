package graph

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var ErrUnsupportedImage = errors.New("unsupported photo format")
var ErrImageTooLarge = errors.New("image exceeds 20 MiB")

const BaseURL = "https://graph.microsoft.com/v1.0"
const Scopes = "offline_access User.Read User.ReadBasic.All Chat.Read ChatMessage.Send"

type RawBody []byte

type Client struct {
	HTTP  *http.Client
	Base  string
	Token func(context.Context) (string, error)
}

func HTTPClient() *http.Client {
	return &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
}

type APIError struct {
	Status     int
	Code       string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	return fmt.Sprintf("Microsoft Graph HTTP %d (%s)", e.Status, e.Code)
}
func (c *Client) Do(ctx context.Context, method, path string, body, out any) error {
	base := c.Base
	if base == "" {
		base = BaseURL
	}
	target := path
	if !strings.HasPrefix(path, "https://") && !strings.HasPrefix(path, "http://") {
		target = base + path
	}
	b, err := url.Parse(base)
	if err != nil {
		return err
	}
	u, err := url.Parse(target)
	if err != nil {
		return err
	}
	if u.Scheme != b.Scheme || u.Host != b.Host || !strings.HasPrefix(u.Path, b.Path+"/") || u.User != nil {
		return fmt.Errorf("refusing untrusted Graph URL")
	}
	var data []byte
	if body != nil {
		if raw, ok := body.(RawBody); ok {
			data = raw
		} else {
			data, err = json.Marshal(body)
		}
		if err != nil {
			return err
		}
	}
	token, err := c.Token(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	if _, ok := body.(RawBody); ok {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	hc := c.HTTP
	if hc == nil {
		hc = HTTPClient()
	}
	res, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("Graph request failed: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var payload struct{ Error struct{ Code string } }
		_ = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&payload)
		sec, _ := strconv.Atoi(res.Header.Get("Retry-After"))
		delay := time.Duration(sec) * time.Second
		if when, e := http.ParseTime(res.Header.Get("Retry-After")); e == nil {
			delay = time.Until(when)
		}
		return &APIError{res.StatusCode, payload.Error.Code, delay}
	}
	if out == nil {
		return nil
	}

	if target, ok := out.(*[]byte); ok {
		data, err := io.ReadAll(io.LimitReader(res.Body, (20<<20)+1))
		if err != nil {
			return err
		}
		if len(data) > 20<<20 {
			return ErrImageTooLarge
		}
		kind := http.DetectContentType(data)
		if kind != "image/jpeg" && kind != "image/png" && kind != "image/gif" {
			return ErrUnsupportedImage
		}
		*target = data
		return nil
	}
	return json.NewDecoder(io.LimitReader(res.Body, 16<<20)).Decode(out)
}
func List[T any](ctx context.Context, c *Client, path string) ([]T, error) {
	var items []T
	seen := map[string]bool{}
	for path != "" {
		if seen[path] {
			return nil, fmt.Errorf("Graph pagination loop")
		}
		seen[path] = true
		var page struct {
			Value []T    `json:"value"`
			Next  string `json:"@odata.nextLink"`
		}
		if err := c.Do(ctx, "GET", path, nil, &page); err != nil {
			return nil, err
		}
		items = append(items, page.Value...)
		path = page.Next
	}
	return items, nil
}

type User struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}
type Member struct {
	UserID      string `json:"userId"`
	DisplayName string `json:"displayName"`
}
type Chat struct {
	Viewpoint *struct {
		LastRead time.Time `json:"lastMessageReadDateTime"`
	} `json:"viewpoint"`
	LastMessage *Message `json:"lastMessagePreview"`
	ID          string   `json:"id"`
	Topic       string   `json:"topic"`
	ChatType    string   `json:"chatType"`
	Members     []Member `json:"members"`
}
type Mention struct {
	ID        int    `json:"id"`
	Text      string `json:"mentionText"`
	Mentioned struct {
		User         *User `json:"user,omitempty"`
		Conversation *struct {
			ID   string `json:"id"`
			Type string `json:"conversationIdentityType"`
		} `json:"conversation,omitempty"`
	} `json:"mentioned"`
}
type Reaction struct {
	DisplayName string    `json:"displayName"`
	ContentURL  string    `json:"reactionContentUrl"`
	Type        string    `json:"reactionType"`
	Created     time.Time `json:"createdDateTime"`
	User        struct {
		User *User `json:"user"`
	} `json:"user"`
}
type Message struct {
	Edited      *time.Time `json:"lastEditedDateTime"`
	Reactions   []Reaction `json:"reactions"`
	ReplyToID   string     `json:"replyToId"`
	Subject     string     `json:"subject"`
	Replies     []Message  `json:"replies"`
	RepliesNext string     `json:"replies@odata.nextLink"`
	Mentions    []Mention  `json:"mentions"`
	ID          string     `json:"id"`
	Created     time.Time  `json:"createdDateTime"`
	Modified    time.Time  `json:"lastModifiedDateTime"`
	Deleted     *time.Time `json:"deletedDateTime"`
	MessageType string     `json:"messageType"`
	Body        struct {
		ContentType string `json:"contentType"`
		Content     string `json:"content"`
	} `json:"body"`
	From struct {
		User        *User `json:"user"`
		Application *User `json:"application"`
	} `json:"from"`
	Attachments []struct {
		ID          string `json:"id"`
		ContentType string `json:"contentType"`
		Content     string `json:"content"`
		Name        string `json:"name"`
		ContentURL  string `json:"contentUrl"`
	} `json:"attachments"`
}

func ChatPath(id string) string { return "/chats/" + url.PathEscape(id) }
