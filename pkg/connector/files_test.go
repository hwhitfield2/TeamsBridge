package connector

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
	"teamsbridge.local/teamsbridge/internal/graph"
)

type fileIntent struct {
	bridgev2.MatrixAPI
	data string
}

func (f *fileIntent) DownloadMediaToFile(ctx context.Context, uri id.ContentURIString, encrypted *event.EncryptedFileInfo, cache bool, callback func(*os.File) error) error {
	tmp, err := os.CreateTemp("", "teams-file-test")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	tmp.WriteString(f.data)
	tmp.Seek(0, 0)
	return callback(tmp)
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestFileUploadExactRecipients(t *testing.T) {
	stages := []string{}
	c, p := mockActions(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/members"):
			stages = append(stages, "members")
			w.Write([]byte(`{"value":[{"userId":"self"},{"userId":"coworker"}]}`))
		case r.Method == "PUT":
			stages = append(stages, "upload")
			data, _ := io.ReadAll(r.Body)
			if string(data) != "file data" || r.Header.Get("Content-Type") != "application/octet-stream" {
				t.Error("wrong binary upload")
			}
			json.NewEncoder(w).Encode(map[string]any{"id": "item", "webUrl": "https://tenant.sharepoint.com/file", "parentReference": map[string]string{"driveId": "drive"}})
		case strings.HasSuffix(r.URL.Path, "/invite"):
			stages = append(stages, "share")
			var v struct {
				Recipients     []map[string]string
				Roles          []string
				SendInvitation bool
				RequireSignIn  bool
			}
			json.NewDecoder(r.Body).Decode(&v)
			if len(v.Recipients) != 1 || v.Recipients[0]["objectId"] != "coworker" || v.SendInvitation || !v.RequireSignIn || v.Roles[0] != "read" {
				t.Error("incorrect file sharing", v)
			}
			w.Write([]byte(`{"value":[{"id":"permission"}]}`))
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/items/item"):
			w.Write([]byte(`{"webDavUrl":"https://tenant.sharepoint.com/report.pdf","eTag":"{11111111-1111-4111-8111-111111111111},1"}`))
		default:
			t.Error(r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	})
	c.meta.Token.Scope += " Files.ReadWrite"
	c.main = &Connector{Bridge: &bridgev2.Bridge{Bot: &fileIntent{data: "file data"}}}
	msg := &bridgev2.MatrixMessage{MatrixEventBase: bridgev2.MatrixEventBase[*event.MessageEventContent]{Portal: p, Content: &event.MessageEventContent{MsgType: event.MsgFile, Body: "Report.pdf", URL: "mxc://test/file"}}}
	payload, path, err := c.outgoingFile(context.Background(), msg)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/chats/chat/messages" || strings.Join(stages, ",") != "members,upload,share" {
		t.Fatal(path, stages)
	}
	attachment := payload.(map[string]any)["attachments"].([]map[string]string)[0]
	if attachment["name"] != "Report.pdf" || attachment["contentType"] != "reference" {
		t.Fatal(attachment)
	}
}
func TestFileDownloadDoesNotLeakGraphToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1.0/shares/u!") {
			t.Error(r.URL.Path)
		}
		w.Write([]byte(`{"id":"file","name":"report.pdf","size":4,"file":{"mimeType":"application/pdf"},"@microsoft.graph.downloadUrl":"https://tenant.sharepoint.com/download?ticket=secret"}`))
	}))
	defer server.Close()
	c := &Client{api: &graph.Client{Base: server.URL + "/v1.0", HTTP: server.Client(), Token: func(context.Context) (string, error) { return "token", nil }}, fileHTTP: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "" {
			t.Error("token leaked to download URL")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("data")), Header: http.Header{}}, nil
	})}}
	item, data, err := c.downloadFile(context.Background(), "https://tenant.sharepoint.com/report.pdf")
	if err != nil || item.Name != "report.pdf" || string(data) != "data" {
		t.Fatal(item.Name, string(data), err)
	}
}
