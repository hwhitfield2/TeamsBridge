package connector

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"teamsbridge.local/teamsbridge/internal/graph"
)

// Microsoft currently documents user-wide delegated message subscriptions on
// beta. Message retrieval and channel subscriptions remain on v1.0.
func (c *Client) subscriptionAPI(resource string) *graph.Client {
	if !strings.HasPrefix(resource, "/users/") {
		return c.api
	}
	api := *c.api
	if api.Base == "" {
		api.Base = "https://graph.microsoft.com/beta"
	} else {
		api.Base = strings.TrimSuffix(api.Base, "/v1.0") + "/beta"
	}
	return &api
}
func (c *Client) saveSubscription(ctx context.Context, resource string, s GraphSubscription) error {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	c.meta.mu.Lock()
	if c.meta.Subscriptions == nil {
		c.meta.Subscriptions = map[string]GraphSubscription{}
	}
	if s.ID == "" {
		delete(c.meta.Subscriptions, resource)
	} else {
		c.meta.Subscriptions[resource] = s
	}
	c.meta.mu.Unlock()
	return c.login.Save(ctx)
}
func (c *Client) ensureWebhookSecret(ctx context.Context) error {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	c.meta.mu.Lock()
	if c.meta.WebhookSecret != "" {
		c.meta.mu.Unlock()
		return nil
	}
	c.meta.WebhookSecret = randomWebhookID()
	c.meta.mu.Unlock()
	return c.login.Save(ctx)
}
func (c *Client) reconcileSubscriptions(ctx context.Context) error {
	c.subscriptionMu.Lock()
	defer c.subscriptionMu.Unlock()
	if err := c.ensureWebhookSecret(ctx); err != nil {
		return err
	}
	resources := []string{"/users/" + url.PathEscape(c.meta.UserID) + "/chats/getAllMessages"}
	for _, ch := range c.main.Config.Channels {
		if ch.TenantID == strings.SplitN(string(c.login.ID), ":", 2)[0] {
			p, _ := channelPath(ch.portalID())
			resources = append(resources, p+"/messages")
		}
	}
	// Only manage subscriptions owned by this login; removed channels stop
	// receiving push updates when their configuration is removed.
	desired := map[string]bool{}
	for _, resource := range resources {
		desired[resource] = true
	}
	c.meta.mu.Lock()
	stale := map[string]GraphSubscription{}
	for resource, sub := range c.meta.Subscriptions {
		if !desired[resource] {
			stale[resource] = sub
		}
	}
	c.meta.mu.Unlock()
	for resource, sub := range stale {
		err := c.subscriptionAPI(resource).Do(ctx, "DELETE", "/subscriptions/"+url.PathEscape(sub.ID), nil, nil)
		var ge *graph.APIError
		if err != nil && !(errors.As(err, &ge) && ge.Status == 404) {
			return err
		}
		if err = c.saveSubscription(ctx, resource, GraphSubscription{}); err != nil {
			return err
		}
	}
	var first error
	for _, resource := range resources {
		if err := c.ensureSubscription(ctx, resource); err != nil {
			c.login.Log.Warn().Err(err).Str("resource", resource).Msg("Teams push subscription unavailable; polling remains active")
			if first == nil {
				first = err
			}
		}
	}
	return first
}
func (c *Client) ensureSubscription(ctx context.Context, resource string) error {
	c.meta.mu.Lock()
	s := c.meta.Subscriptions[resource]
	secret := c.meta.WebhookSecret
	c.meta.mu.Unlock()
	api := c.subscriptionAPI(resource)
	endpoint := c.main.Config.WebhookURL + webhookPath
	if s.ID != "" && s.URL != endpoint {
		err := api.Do(ctx, "DELETE", "/subscriptions/"+url.PathEscape(s.ID), nil, nil)
		var ge *graph.APIError
		if err != nil && !(errors.As(err, &ge) && ge.Status == 404) {
			return err
		}
		if err = c.saveSubscription(ctx, resource, GraphSubscription{}); err != nil {
			return err
		}
		s = GraphSubscription{}
	}
	// Renew often enough to refresh the webhook authorization as well as expiry.
	if s.ID != "" && time.Until(s.Expires) > 90*time.Minute {
		return nil
	}
	expiration := time.Now().UTC().Add(2 * time.Hour)
	if s.ID != "" {
		var renewed GraphSubscription
		err := api.Do(ctx, "PATCH", "/subscriptions/"+url.PathEscape(s.ID), map[string]any{"expirationDateTime": expiration}, &renewed)
		if err == nil {
			s.Expires = renewed.Expires
			if s.Expires.IsZero() {
				return fmt.Errorf("subscription renewal missing expiry")
			}
			return c.saveSubscription(ctx, resource, s)
		}
		var ge *graph.APIError
		if !errors.As(err, &ge) || ge.Status != 404 {
			return err
		}
		if err = c.saveSubscription(ctx, resource, GraphSubscription{}); err != nil {
			return err
		}
	}
	// Recover a successful creation whose response/save was interrupted. Only
	// adopt subscriptions with our random clientState and exact destination.
	existing, err := graph.List[GraphSubscription](ctx, api, "/subscriptions")
	if err != nil {
		return err
	}
	for _, found := range existing {
		if strings.TrimPrefix(found.Resource, "/") == strings.TrimPrefix(resource, "/") && found.URL == endpoint && found.ClientState == secret && time.Until(found.Expires) > 0 {
			found.Resource = resource
			if err = c.saveSubscription(ctx, resource, found); err != nil {
				return err
			}
			return nil
		}
	}
	payload := map[string]any{"changeType": "created,updated,deleted", "resource": resource, "notificationUrl": endpoint, "lifecycleNotificationUrl": endpoint, "includeResourceData": false, "expirationDateTime": expiration, "clientState": secret}
	var created GraphSubscription
	if err = api.Do(ctx, "POST", "/subscriptions", payload, &created); err != nil {
		return err
	}
	if created.ID == "" || created.Expires.IsZero() {
		return fmt.Errorf("subscription response missing ID or expiry")
	}
	created.Resource = resource
	created.URL = endpoint
	created.ClientState = secret
	if err = c.saveSubscription(ctx, resource, created); err != nil {
		return err
	}
	c.login.Log.Info().Str("resource", resource).Time("expires", created.Expires).Msg("Teams push subscription active")
	return nil
}
func (c *Client) subscriptionLoop(ctx context.Context) {
	for ctx.Err() == nil {
		err := c.reconcileSubscriptions(ctx)
		delay := 5 * time.Minute
		if err != nil {
			delay = time.Minute
			var ge *graph.APIError
			if errors.As(err, &ge) && ge.RetryAfter > delay {
				delay = ge.RetryAfter
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
