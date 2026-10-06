package connector

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const webhookPath = "/graph/notifications"

type GraphSubscription struct {
	ID          string    `json:"id"`
	Resource    string    `json:"resource"`
	URL         string    `json:"notificationUrl"`
	ClientState string    `json:"clientState,omitempty"`
	Expires     time.Time `json:"expirationDateTime"`
}

type graphNotification struct {
	SubscriptionID string `json:"subscriptionId"`
	TenantID       string `json:"tenantId"`
	ClientState    string `json:"clientState"`
	Resource       string `json:"resource"`
	Change         string `json:"changeType"`
	Lifecycle      string `json:"lifecycleEvent"`
}

type notificationTarget struct{ Portal, Message, ReplyTo, Path string }

var odataSegment = regexp.MustCompile(`^([a-zA-Z]+)\('([^']+)'\)$`)

// Notifications supply identifiers, never arbitrary URLs to fetch with a token.
func parseNotificationTarget(resource string) (notificationTarget, error) {
	var target notificationTarget
	if len(resource) > 4096 {
		return target, fmt.Errorf("resource too long")
	}
	pieces := strings.Split(strings.TrimPrefix(resource, "/"), "/")
	var p []string
	for _, s := range pieces {
		if m := odataSegment.FindStringSubmatch(s); m != nil {
			p = append(p, m[1], m[2])
		} else {
			p = append(p, s)
		}
	}
	for i := 1; i < len(p); i += 2 {
		v, err := url.PathUnescape(p[i])
		if err != nil || v == "" || strings.ContainsAny(v, "/\\?#\x00\r\n") || v == "." || v == ".." {
			return target, fmt.Errorf("invalid resource identifier")
		}
		p[i] = v
	}
	if len(p) == 4 && p[0] == "chats" && p[2] == "messages" {
		target.Portal = p[1]
		target.Message = p[3]
	} else if (len(p) == 6 || len(p) == 8) && p[0] == "teams" && p[2] == "channels" && p[4] == "messages" {
		target.Portal = "channel:" + p[1] + ":" + p[3]
		target.Message = p[5]
		if len(p) == 8 {
			if p[6] != "replies" {
				return target, fmt.Errorf("invalid reply resource")
			}
			target.ReplyTo = p[5]
			target.Message = p[7]
		}
	} else {
		return target, fmt.Errorf("unsupported notification resource")
	}
	for i := 1; i < len(p); i += 2 {
		p[i] = url.PathEscape(p[i])
	}
	target.Path = "/" + strings.Join(p, "/")
	return target, nil
}

func randomWebhookID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func (c *Connector) startWebhook() error {
	if c.Config.WebhookURL == "" {
		return nil
	}
	u, err := url.Parse(c.Config.WebhookURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return fmt.Errorf("webhook_url must be a public HTTPS origin, without a path or query")
	}
	c.Config.WebhookURL = strings.TrimRight(c.Config.WebhookURL, "/")
	if c.Config.WebhookListen == "" {
		c.Config.WebhookListen = "127.0.0.1:29320"
	}
	host, _, err := net.SplitHostPort(c.Config.WebhookListen)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return fmt.Errorf("webhook_listen must bind a loopback IP and port")
	}
	if _, err = c.Bridge.DB.Exec(context.Background(), `CREATE TABLE IF NOT EXISTS teams_webhook_queue (bridge_id TEXT NOT NULL, login_id TEXT NOT NULL, id TEXT NOT NULL, payload TEXT NOT NULL, received BIGINT NOT NULL, retry_at BIGINT NOT NULL, PRIMARY KEY (bridge_id, login_id, id))`); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", c.Config.WebhookListen)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc(webhookPath, c.handleWebhook)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "ok\n")
	})
	c.webServer = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() {
		if err := c.webServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			c.Bridge.Log.Error().Err(err).Msg("Teams webhook listener stopped; polling remains active")
		}
	}()
	c.Bridge.Log.Info().Str("listen", c.Config.WebhookListen).Msg("Teams webhook listener started")
	return nil
}
func (c *Connector) Stop() {
	if c.webServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = c.webServer.Shutdown(ctx)
	}
}

func (c *Connector) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if token := r.URL.Query().Get("validationToken"); token != "" {
		if len(token) > 8192 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = io.WriteString(w, token)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var batch struct {
		Value []graphNotification `json:"value"`
	}
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&batch); err != nil || len(batch.Value) == 0 || len(batch.Value) > 1000 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if dec.Decode(new(any)) != io.EOF {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	for _, n := range batch.Value {
		var owner *Client
		c.webClients.Range(func(_, v any) bool {
			client := v.(*Client)
			if client.acceptNotification(n) {
				owner = client
				return false
			}
			return true
		})
		// Invalid secrets, tenants and resources never cause Graph reads or mutations.
		if owner == nil {
			continue
		}
		raw, _ := json.Marshal(n)
		if _, err := c.Bridge.DB.Exec(ctx, `INSERT INTO teams_webhook_queue (bridge_id,login_id,id,payload,received,retry_at) VALUES ($1,$2,$3,$4,$5,0)`, c.Bridge.ID, owner.login.ID, randomWebhookID(), string(raw), time.Now().UnixMilli()); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		select {
		case owner.webWake <- struct{}{}:
		default:
		}
	}
	w.WriteHeader(http.StatusAccepted)
}

func (c *Client) acceptNotification(n graphNotification) bool {
	c.meta.mu.Lock()
	defer c.meta.mu.Unlock()
	if c.meta.WebhookSecret == "" || subtle.ConstantTimeCompare([]byte(n.ClientState), []byte(c.meta.WebhookSecret)) != 1 || n.TenantID != strings.SplitN(string(c.login.ID), ":", 2)[0] {
		return false
	}
	var subscription GraphSubscription
	for _, s := range c.meta.Subscriptions {
		if s.ID == n.SubscriptionID {
			subscription = s
			break
		}
	}
	if subscription.ID == "" {
		return false
	}
	if n.Lifecycle != "" {
		return n.Lifecycle == "reauthorizationRequired" || n.Lifecycle == "subscriptionRemoved" || n.Lifecycle == "missed"
	}
	if n.Change != "created" && n.Change != "updated" && n.Change != "deleted" {
		return false
	}
	target, err := parseNotificationTarget(n.Resource)
	if err != nil {
		return false
	}
	if strings.HasPrefix(subscription.Resource, "/users/") {
		return !strings.HasPrefix(target.Portal, "channel:")
	}
	path, ok := channelPath(target.Portal)
	return ok && subscription.Resource == path+"/messages"
}
