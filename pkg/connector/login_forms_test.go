package connector

import (
	"context"
	"strings"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
)

func TestPermissionProfiles(t *testing.T) {
	for _, profile := range []string{"readonly", "messaging", "photos"} {
		s := LoginSettings{Profile: profile}
		scopes := s.oauth().Scopes()
		if strings.Contains(scopes, "ChatMessage.Send") != (profile != "readonly") || strings.Contains(scopes, "User.ReadBasic.All") != (profile == "photos") {
			t.Fatal(profile, scopes)
		}
	}
}
func TestConditionalLoginForms(t *testing.T) {
	c := &Connector{Config: Config{ClientID: "11111111-1111-1111-1111-111111111111", TenantID: "22222222-2222-2222-2222-222222222222"}}
	for _, flow := range c.GetLoginFlows() {
		process, err := c.CreateLogin(context.Background(), nil, flow.ID)
		if err != nil {
			t.Fatal(err)
		}
		l := process.(*Login)
		step, err := l.Start(context.Background())
		if err != nil || step.Type != bridgev2.LoginStepTypeUserInput {
			t.Fatal(step, err)
		}
		want := 3
		if flow.ID == "work_device_code" {
			want = 2
		}
		if len(step.UserInputParams.Fields) != want {
			t.Fatal(flow.ID)
		}
		if flow.ID == "work_device_code" {
			continue
		}
		step, err = l.SubmitUserInput(context.Background(), map[string]string{"client_id": c.Config.ClientID, "tenant_id": c.Config.TenantID, "authentication": "new_secret"})
		if err != nil || step.StepID != "teamswork.secret" || step.UserInputParams.Fields[0].Type != bridgev2.LoginInputFieldTypePassword {
			t.Fatal(step, err)
		}
		if _, err = l.SubmitUserInput(context.Background(), map[string]string{"client_secret": ""}); err == nil {
			t.Fatal("empty secret accepted")
		}
	}
}
func TestSavedSecretCannotCrossApps(t *testing.T) {
	c := &Connector{Config: Config{ClientID: "11111111-1111-1111-1111-111111111111", TenantID: "22222222-2222-2222-2222-222222222222"}}
	p, _ := c.CreateLogin(context.Background(), nil, "work_browser")
	_, err := p.(*Login).SubmitUserInput(context.Background(), map[string]string{"client_id": "33333333-3333-3333-3333-333333333333", "tenant_id": c.Config.TenantID, "authentication": "saved_secret"})
	if err == nil {
		t.Fatal("saved secret sent to another registration")
	}
}
