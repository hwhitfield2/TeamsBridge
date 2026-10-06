package connector

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"

	"teamsbridge.local/teamsbridge/internal/graph"
)

func (c *Client) webhookLoop(ctx context.Context) {
	done := make(chan struct{})
	go func() { defer close(done); c.subscriptionLoop(ctx) }()
	defer func() { <-done }()
	for ctx.Err() == nil {
		if worked, err := c.processQueuedNotification(ctx); err != nil {
			c.login.Log.Warn().Err(err).Msg("Teams webhook queue failed")
		} else if worked {
			continue
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-c.webWake:
			timer.Stop()
		case <-timer.C:
		}
	}
}
func (c *Client) processQueuedNotification(ctx context.Context) (bool, error) {
	var key, payload string
	err := c.main.Bridge.DB.QueryRow(ctx, `SELECT id,payload FROM teams_webhook_queue WHERE bridge_id=$1 AND login_id=$2 AND retry_at<=$3 ORDER BY received,id LIMIT 1`, c.main.Bridge.ID, c.login.ID, time.Now().UnixMilli()).Scan(&key, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var n graphNotification
	if err = json.Unmarshal([]byte(payload), &n); err == nil {
		err = c.processNotification(ctx, n)
	}
	if err != nil {
		delay := time.Minute
		var ge *graph.APIError
		if errors.As(err, &ge) && ge.RetryAfter > delay {
			delay = ge.RetryAfter
		}
		_, saveErr := c.main.Bridge.DB.Exec(ctx, `UPDATE teams_webhook_queue SET retry_at=$1 WHERE bridge_id=$2 AND login_id=$3 AND id=$4`, time.Now().Add(delay).UnixMilli(), c.main.Bridge.ID, c.login.ID, key)
		if saveErr != nil {
			return false, saveErr
		}
		c.login.Log.Warn().Err(err).Msg("Teams push update will retry; notification retained")
		return true, nil
	}
	_, err = c.main.Bridge.DB.Exec(ctx, `DELETE FROM teams_webhook_queue WHERE bridge_id=$1 AND login_id=$2 AND id=$3`, c.main.Bridge.ID, c.login.ID, key)
	return true, err
}
func (c *Client) processNotification(ctx context.Context, n graphNotification) error {
	if n.Lifecycle != "" {
		return c.processLifecycle(ctx, n)
	}
	target, err := parseNotificationTarget(n.Resource)
	if err != nil {
		return err
	}
	v, _ := c.chatSync.LoadOrStore(target.Portal, &sync.Mutex{})
	lock := v.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()
	m, err := c.fetchNotificationMessage(ctx, target, n.Change)
	if err != nil {
		return err
	}
	if err = c.queueMessage(ctx, target.Portal, m, true); err != nil {
		return err
	}
	c.login.Log.Info().Str("change", n.Change).Msg("Teams push update delivered")
	return nil
}
func (c *Client) fetchNotificationMessage(ctx context.Context, target notificationTarget, change string) (graph.Message, error) {
	var m graph.Message
	err := c.api.Do(ctx, "GET", target.Path, nil, &m)
	if err != nil {
		var ge *graph.APIError
		if change != "deleted" || !errors.As(err, &ge) || ge.Status != 404 {
			return m, err
		}
		now := time.Now()
		m.ID = target.Message
		m.Deleted = &now
	}
	if m.ID != target.Message {
		return m, fmt.Errorf("notification fetch returned a different or missing message ID")
	}
	if m.ReplyToID == "" {
		m.ReplyToID = target.ReplyTo
	}
	return m, nil
}

func (c *Client) processLifecycle(ctx context.Context, n graphNotification) error {
	c.subscriptionMu.Lock()
	c.meta.mu.Lock()
	var resource string
	var sub GraphSubscription
	for key, s := range c.meta.Subscriptions {
		if s.ID == n.SubscriptionID {
			resource = key
			sub = s
			break
		}
	}
	c.meta.mu.Unlock()
	if resource == "" {
		c.subscriptionMu.Unlock()
		return nil
	}
	var err error
	switch n.Lifecycle {
	case "reauthorizationRequired":
		err = c.subscriptionAPI(resource).Do(ctx, "POST", "/subscriptions/"+url.PathEscape(sub.ID)+"/reauthorize", nil, nil)
	case "subscriptionRemoved":
		err = c.saveSubscription(ctx, resource, GraphSubscription{})
		if err == nil {
			err = c.ensureSubscription(ctx, resource)
		}
	}
	c.subscriptionMu.Unlock()
	if err != nil {
		return err
	}
	if n.Lifecycle == "missed" || n.Lifecycle == "subscriptionRemoved" {
		// Recover through normal cursor-based sync after a delivery gap.
		if err = c.sync(ctx); err != nil {
			return err
		}
		for _, ch := range c.main.Config.Channels {
			if ch.TenantID == n.TenantID {
				if err = c.syncChannel(ctx, ch); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
