package connector

import (
	"context"
	"net/http"
	"net/http/httptest"
	"teamsbridge.local/teamsbridge/internal/graph"
	"testing"
)

func TestProfilePhotosAndFallback(t *testing.T) {
	for _, status := range []int{200, 403, 404} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(status)
				if status == 200 {
					w.Write([]byte{137, 80, 78, 71, 13, 10, 26, 10})
				}
			}))
			defer s.Close()
			c := &Client{api: &graph.Client{Base: s.URL + "/v1.0", HTTP: s.Client(), Token: func(context.Context) (string, error) { return "test", nil }}}
			for i := 0; i < 2; i++ {
				got := c.profile(context.Background(), "user", "Colleague")
				if got.Name == nil || *got.Name != "Colleague" {
					t.Fatal("lost display name")
				}
				if (got.Avatar != nil) != (status == 200) {
					t.Fatal("incorrect photo fallback")
				}
			}
			if calls != 1 {
				t.Fatal("photo results were not cached")
			}
		})
	}
}
