package connector

import (
	"errors"
	"testing"
	"time"

	"teamsbridge.local/teamsbridge/internal/graph"
)

func TestRetainedEditDoesNotBlockNewMessages(t *testing.T) {
	pending := &deferredMessageError{cause: errMessageLayout}
	var delivered []string
	messages := []graph.Message{{ID: "old-edit"}, {ID: "new-1"}, {ID: "new-2"}}
	err := deliverMessageBatch(messages, func(m graph.Message) error {
		if m.ID == "old-edit" {
			return pending
		}
		delivered = append(delivered, m.ID)
		return nil
	})
	if !isDeferredMessage(err) || len(delivered) != 2 || delivered[0] != "new-1" || delivered[1] != "new-2" {
		t.Fatal("new messages blocked or cursor allowed to advance", delivered, err)
	}
	critical := errors.New("database unavailable")
	delivered = nil
	err = deliverMessageBatch(messages, func(m graph.Message) error { delivered = append(delivered, m.ID); return critical })
	if !errors.Is(err, critical) || isDeferredMessage(err) || len(delivered) != 1 {
		t.Fatal("connection/storage failure suppressed", delivered, err)
	}
}
func TestLayoutRetryCooldownDoesNotLoseChanges(t *testing.T) {
	c := &Client{}
	m := graph.Message{ID: "message"}
	m.Body.Content = "old"
	pending := &deferredMessageError{hash: contentHash(m), retryAt: time.Now().Add(time.Minute), cause: errMessageLayout}
	key := messageID("chat", m.ID)
	c.deferredMessages.Store(key, pending)
	if !isDeferredMessage(c.deferredMessage("chat", m)) {
		t.Fatal("unchanged failure retried too soon")
	}
	// A deletion must never wait behind a rendering failure.
	now := time.Now()
	m.Deleted = &now
	if c.deferredMessage("chat", m) != nil {
		t.Fatal("deletion delayed")
	}
	m.Deleted = nil
	m.Body.Content = "new"
	if c.deferredMessage("chat", m) != nil {
		t.Fatal("changed content delayed")
	}
	if _, exists := c.deferredMessages.Load(key); exists {
		t.Fatal("stale failure retained")
	}
	pending.retryAt = time.Now().Add(-time.Minute)
	pending.hash = contentHash(m)
	c.deferredMessages.Store(key, pending)
	if c.deferredMessage("chat", m) != nil {
		t.Fatal("expired retry suppressed")
	}
}
