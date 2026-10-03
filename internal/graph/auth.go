package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type OAuth struct {
	ClientID, TenantID string
	SecretFile         string
	RequestedScopes    string
	HTTP               *http.Client
}

var guid = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func (o OAuth) Validate() error {
	if !guid.MatchString(o.ClientID) || !guid.MatchString(o.TenantID) {
		return fmt.Errorf("client_id and tenant_id must be UUIDs")
	}
	return nil
}

type Token struct {
	Access    string    `json:"access_token"`
	Refresh   string    `json:"refresh_token"`
	ExpiresIn int       `json:"expires_in"`
	ExpiresAt time.Time `json:"expires_at"`
}
type Challenge struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}
type OAuthError struct {
	Code          string `json:"error"`
	ErrorCodes    []int  `json:"error_codes"`
	CorrelationID string `json:"correlation_id"`
	Timestamp     string `json:"timestamp"`
	Description   string `json:"error_description"`
}

func (e *OAuthError) Error() string {
	message := "Microsoft sign-in: " + e.Code
	for _, code := range e.ErrorCodes {
		message += fmt.Sprintf(" (AADSTS%d)", code)
		switch code {
		case 7000218:
			message += ": this app requires a client secret or a public-client redirect registration"
		case 7000215:
			message += ": the client secret is invalid; use its value, not its ID"
		case 7000222:
			message += ": the client secret has expired"
		}
	}
	if guid.MatchString(e.CorrelationID) {
		message += "; correlation ID: " + e.CorrelationID
	}
	return message
}
func (o OAuth) post(ctx context.Context, route string, values url.Values, out any) error {
	if err := o.Validate(); err != nil {
		return err
	}
	values.Set("client_id", o.ClientID)
	if route == "token" && o.SecretFile != "" {
		secret, err := os.ReadFile(o.SecretFile)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("cannot read local client secret file")
		}
		if len(secret) > 0 {
			values.Set("client_secret", strings.TrimSpace(string(secret)))
		}
	}
	req, err := http.NewRequestWithContext(ctx, "POST", "https://login.microsoftonline.com/"+o.TenantID+"/oauth2/v2.0/"+route, strings.NewReader(values.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	hc := o.HTTP
	if hc == nil {
		hc = HTTPClient()
	}
	res, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("Microsoft sign-in request failed: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		var oe OAuthError
		_ = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&oe)
		if oe.Code == "" {
			oe.Code = fmt.Sprintf("HTTP_%d", res.StatusCode)
		}

		// Store only redacted diagnostics, never the request body or returned tokens.
		if o.SecretFile != "" {
			description := oe.Description
			for _, key := range []string{"client_secret", "code", "refresh_token", "code_verifier"} {
				if value := values.Get(key); value != "" {
					description = strings.ReplaceAll(description, value, "[REDACTED]")
				}
			}
			diagnostic := map[string]any{"client_id": o.ClientID, "tenant_id": o.TenantID, "grant_type": values.Get("grant_type"), "redirect_uri": values.Get("redirect_uri"), "secret_present": values.Get("client_secret") != "", "error": oe.Code, "error_codes": oe.ErrorCodes, "description": description, "correlation_id": oe.CorrelationID, "timestamp": oe.Timestamp}
			if data, e := json.MarshalIndent(diagnostic, "", "  "); e == nil {
				_ = os.WriteFile(filepath.Join(filepath.Dir(o.SecretFile), "oauth-diagnostic.json"), data, 0600)
			}
		}
		oe.Description = ""
		return &oe
	}
	return json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(out)
}
func (o OAuth) Start(ctx context.Context) (*Challenge, error) {
	var c Challenge
	err := o.post(ctx, "devicecode", url.Values{"scope": {o.Scopes()}}, &c)
	if err == nil && (c.DeviceCode == "" || c.UserCode == "" || c.ExpiresIn <= 0) {
		return nil, fmt.Errorf("invalid device-code response")
	}
	return &c, err
}
func (o OAuth) Poll(ctx context.Context, c *Challenge) (*Token, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.ExpiresIn)*time.Second)
	defer cancel()
	interval := time.Duration(c.Interval) * time.Second
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	for {
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		var t Token
		err := o.post(ctx, "token", url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}, "device_code": {c.DeviceCode}}, &t)
		if oe, ok := err.(*OAuthError); ok {
			switch oe.Code {
			case "authorization_pending":
				continue
			case "slow_down":
				interval += 5 * time.Second
				continue
			}
		}
		if err != nil {
			return nil, err
		}
		if t.Access == "" || t.Refresh == "" {
			return nil, fmt.Errorf("Microsoft did not return access and refresh tokens")
		}
		t.ExpiresAt = time.Now().Add(time.Duration(t.ExpiresIn) * time.Second)
		return &t, nil
	}
}
func (o OAuth) Refresh(ctx context.Context, refresh string) (*Token, error) {
	var t Token
	err := o.post(ctx, "token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}}, &t)
	if err != nil {
		return nil, err
	}
	if t.Access == "" {
		return nil, fmt.Errorf("empty access token")
	}
	if t.Refresh == "" {
		t.Refresh = refresh
	}
	t.ExpiresAt = time.Now().Add(time.Duration(t.ExpiresIn) * time.Second)
	return &t, nil
}

func (o OAuth) Scopes() string {
	if o.RequestedScopes != "" {
		return o.RequestedScopes
	}
	return Scopes
}
