package connector

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"maunium.net/go/mautrix/bridgev2"
	"teamsbridge.local/teamsbridge/internal/graph"
)

type LoginSettings struct {
	ClientID   string `json:"client_id"`
	TenantID   string `json:"tenant_id"`
	Profile    string `json:"profile"`
	SecretFile string `json:"secret_file,omitempty"`
}

func (s LoginSettings) oauth() graph.OAuth {
	scopes := "offline_access User.Read Chat.Read"
	if s.Profile != "readonly" {
		scopes += " ChatMessage.Send"
	}
	if s.Profile == "photos" {
		scopes += " User.ReadBasic.All"
	}
	return graph.OAuth{ClientID: s.ClientID, TenantID: s.TenantID, SecretFile: s.SecretFile, RequestedScopes: scopes}
}
func (c *Client) oauth() graph.OAuth {
	if c.meta.Auth != nil {
		return c.meta.Auth.oauth()
	}
	return c.main.oauth()
}
func (l *Login) inputStep() *bridgev2.LoginStep {
	fields := []bridgev2.LoginInputDataField{
		{ID: "client_id", Type: bridgev2.LoginInputFieldTypeUsername, Name: "Application (client) ID", DefaultValue: l.settings.ClientID},
		{ID: "tenant_id", Type: bridgev2.LoginInputFieldTypeUsername, Name: "Directory (tenant) ID", DefaultValue: l.settings.TenantID},
	}
	instructions := "Enter the app registration with the delegated permissions approved for this login. Adding a permission without granting consent may still require administrator approval."
	if l.browserFlow {
		fields = append(fields, bridgev2.LoginInputDataField{ID: "authentication", Type: bridgev2.LoginInputFieldTypeSelect, Name: "App registration authentication", DefaultValue: "saved_secret", Options: []string{"saved_secret", "new_secret", "public_client"}, Description: "saved_secret uses this Mac's configured app; new_secret prompts next; public_client requires a public-client redirect registration."})
		instructions += " Register http://localhost:29319/oauth/callback as the redirect URI."
	}
	return &bridgev2.LoginStep{Type: bridgev2.LoginStepTypeUserInput, StepID: "teamswork.registration", Instructions: instructions, UserInputParams: &bridgev2.LoginUserInputParams{Fields: fields}}
}
func (l *Login) SubmitUserInput(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	l.mu.Lock()
	canceled := l.canceled
	l.mu.Unlock()
	if canceled {
		return nil, context.Canceled
	}
	if l.configured {
		return nil, fmt.Errorf("login already started")
	}
	if l.awaitSecret {
		secret := strings.TrimSpace(input["client_secret"])
		if secret == "" {
			return nil, fmt.Errorf("enter the client secret value, not its ID")
		}
		if err := os.MkdirAll(".secrets", 0700); err != nil {
			return nil, err
		}
		f, err := os.CreateTemp(".secrets", "login-secret-*")
		if err != nil {
			return nil, err
		}
		if _, err = f.WriteString(secret); err != nil {
			f.Close()
			os.Remove(f.Name())
			return nil, err
		}
		if err = f.Close(); err != nil {
			os.Remove(f.Name())
			return nil, err
		}
		l.settings.SecretFile = filepath.Clean(f.Name())
		l.createdSecret = true
		l.awaitSecret = false
	} else {
		s := l.settings
		s.ClientID = strings.TrimSpace(input["client_id"])
		s.TenantID = strings.TrimSpace(input["tenant_id"])
		if err := s.oauth().Validate(); err != nil {
			return nil, err
		}
		if l.browserFlow {
			switch input["authentication"] {
			case "saved_secret":
				if s.ClientID != l.main.Config.ClientID || s.TenantID != l.main.Config.TenantID {
					return nil, fmt.Errorf("saved secret belongs to the configured registration; select new_secret for another app")
				}
				s.SecretFile = ".secrets/client-secret"
			case "new_secret":
				l.settings = s
				l.awaitSecret = true
				return &bridgev2.LoginStep{Type: bridgev2.LoginStepTypeUserInput, StepID: "teamswork.secret", Instructions: "Enter the client secret VALUE for this Web app registration. It is stored in a private local file on this Mac.", UserInputParams: &bridgev2.LoginUserInputParams{Fields: []bridgev2.LoginInputDataField{{ID: "client_secret", Type: bridgev2.LoginInputFieldTypePassword, Name: "Client secret value"}}}}, nil
			case "public_client":
				s.SecretFile = ""
			default:
				return nil, fmt.Errorf("select an authentication method")
			}
		}
		l.settings = s
	}
	l.configured = true
	return l.Start(ctx)
}

var _ bridgev2.LoginProcessUserInput = (*Login)(nil)
