package connector

import (
	"errors"
	"time"

	"teamsbridge.local/teamsbridge/internal/graph"
)

var errMessageLayout = errors.New("cannot insert additional parts into an existing Matrix message; rebuild the Beeper chat to repair its layout")

// A retained, message-local failure is not a lost connection. Its unchanged
// cursor and webhook queue entry remain the durable sources for retries.
type deferredMessageError struct {
	hash    string
	retryAt time.Time
	cause   error
}

func (e *deferredMessageError) Error() string { return e.cause.Error() }
func (e *deferredMessageError) Unwrap() error { return e.cause }
func isDeferredMessage(err error) bool {
	var pending *deferredMessageError
	return errors.As(err, &pending)
}
func (c *Client) deferredMessage(portal string, m graph.Message) error {
	if m.Deleted != nil {
		return nil
	}
	key := messageID(portal, m.ID)
	value, ok := c.deferredMessages.Load(key)
	if !ok {
		return nil
	}
	pending := value.(*deferredMessageError)
	if pending.hash == contentHash(m) && time.Now().Before(pending.retryAt) {
		return pending
	}
	c.deferredMessages.Delete(key)
	return nil
}

// Continue past retained rendering failures so a stale edit cannot stop newer
// messages. Return the failure so the caller must not advance its cursor.
func deliverMessageBatch(messages []graph.Message, deliver func(graph.Message) error) error {
	var pending error
	for _, m := range messages {
		if err := deliver(m); err != nil {
			if !isDeferredMessage(err) {
				return err
			}
			if pending == nil {
				pending = err
			}
		}
	}
	return pending
}
