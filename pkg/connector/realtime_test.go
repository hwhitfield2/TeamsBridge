package connector

import (
	"teamsbridge.local/teamsbridge/internal/graph"
	"testing"
	"time"
)

func TestRecentPollingDetectsChangesAndPeriodicRecheck(t *testing.T) {
	now := time.Now()
	chat := graph.Chat{LastMessage: &graph.Message{ID: "current"}}
	for _, test := range []struct {
		name     string
		previous recentStamp
		want     bool
	}{
		{"first discovery", recentStamp{}, true},
		{"unchanged chat", recentStamp{"current", now.Add(-5 * time.Second)}, false},
		{"new message", recentStamp{"old", now.Add(-5 * time.Second)}, true},
		{"periodic catchup", recentStamp{"current", now.Add(-time.Minute)}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := needsRecentSync(chat, test.previous, now); got != test.want {
				t.Fatalf("got %v want %v", got, test.want)
			}
		})
	}
}
