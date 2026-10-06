package connector

import (
	"context"
	"fmt"
	up "go.mau.fi/util/configupgrade"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"net/http"
	"os"
	"strings"
	"sync"
	"teamsbridge.local/teamsbridge/internal/graph"
	"time"
)

type Config struct {
	WebhookURL      string          `yaml:"webhook_url"`
	WebhookListen   string          `yaml:"webhook_listen"`
	Channels        []ChannelConfig `yaml:"channels"`
	ProfilePhotos   bool            `yaml:"profile_photos"`
	ClientID        string          `yaml:"client_id"`
	TenantID        string          `yaml:"tenant_id"`
	PollSeconds     int             `yaml:"poll_seconds"`
	InitialMessages int             `yaml:"initial_messages"`
}

const example = `client_id: YOUR-APPLICATION-CLIENT-ID
tenant_id: YOUR-DIRECTORY-TENANT-ID
poll_seconds: 5
webhook_url: ""
webhook_listen: 127.0.0.1:29320
initial_messages: 50
profile_photos: false
channels: []
`

type Connector struct {
	webServer  *http.Server
	webClients sync.Map
	Bridge     *bridgev2.Bridge
	Config     Config
}
type Metadata struct {
	WebhookSecret     string                       `json:"webhook_secret,omitempty"`
	Subscriptions     map[string]GraphSubscription `json:"subscriptions,omitempty"`
	Media             map[string]string            `json:"media,omitempty"`
	RenderingRevision int                          `json:"rendering_revision,omitempty"`
	mu                sync.Mutex
	Auth              *LoginSettings       `json:"auth,omitempty"`
	Token             graph.Token          `json:"token"`
	UserID            string               `json:"user_id"`
	Cursors           map[string]time.Time `json:"cursors"`
}

var _ bridgev2.NetworkConnector = (*Connector)(nil)

func (c *Connector) Init(b *bridgev2.Bridge) { c.Bridge = b }
func (c *Connector) Start(ctx context.Context) error {
	if err := c.oauth().Validate(); err != nil {
		return err
	}
	if c.Config.PollSeconds < 5 {
		return fmt.Errorf("poll_seconds must be at least 5")
	}
	if c.Config.InitialMessages < 1 || c.Config.InitialMessages > 50 {
		return fmt.Errorf("initial_messages must be 1–50")
	}
	return c.startWebhook()
}
func (c *Connector) oauth() graph.OAuth {
	return graph.OAuth{ClientID: c.Config.ClientID, TenantID: c.Config.TenantID, SecretFile: ".secrets/client-secret"}
}
func (c *Connector) GetName() bridgev2.BridgeName {
	return bridgev2.BridgeName{DisplayName: "Microsoft Teams", NetworkURL: "https://teams.microsoft.com", NetworkID: "teamswork", NetworkIcon: "mxc://local.beeper.com/hwhitfield2_ovaP7Gn0Oiq2cJtYSUBOkm3FY6M0eWVShYOEVWZdjg99s94pyQSI62cVqcbBY8Zx", BeeperBridgeType: "teamswork", DefaultPort: 29349, DefaultCommandPrefix: "!teamswork"}
}
func (c *Connector) GetConfig() (string, any, up.Upgrader) {
	return example, &c.Config, up.SimpleUpgrader(func(h up.Helper) {
		h.Copy(up.Str, "client_id")
		h.Copy(up.Str, "tenant_id")
		h.Copy(up.Int, "poll_seconds")
		h.Copy(up.Str, "webhook_url")
		h.Copy(up.Str, "webhook_listen")
		h.Copy(up.Int, "initial_messages")
		h.Copy(up.Bool, "profile_photos")
		h.Copy(up.List, "channels")
	})
}
func (c *Connector) GetDBMetaTypes() database.MetaTypes {
	return database.MetaTypes{Message: func() any { return &MessageMetadata{} }, UserLogin: func() any { return &Metadata{} }}
}
func (c *Connector) GetCapabilities() *bridgev2.NetworkGeneralCapabilities {
	return &bridgev2.NetworkGeneralCapabilities{Provisioning: bridgev2.ProvisioningCapabilities{ResolveIdentifier: bridgev2.ResolveIdentifierCapabilities{CreateDM: true, LookupEmail: true, LookupUsername: true}, GroupCreation: map[string]bridgev2.GroupTypeCapabilities{"group": {TypeDescription: "Teams group chat", Name: bridgev2.GroupFieldCapability{Allowed: true}, Participants: bridgev2.GroupFieldCapability{Allowed: true, Required: true, MinLength: 1, MaxLength: 249}}}}}
}
func (c *Connector) GetBridgeInfoVersion() (int, int) { return 3, 6 }
func (c *Connector) LoadUserLogin(ctx context.Context, l *bridgev2.UserLogin) error {
	m, ok := l.Metadata.(*Metadata)
	if !ok {
		return fmt.Errorf("invalid Teams login metadata")
	}
	client := &Client{main: c, login: l, meta: m}
	client.api = &graph.Client{Token: client.token}
	client.logged.Store(m.Token.Refresh != "")
	l.Client = client
	return nil
}
func (c *Connector) GetLoginFlows() []bridgev2.LoginFlow {
	return []bridgev2.LoginFlow{
		{ID: "work_browser", Name: "Messaging — browser", Description: "Chat.Read and ChatMessage.Send. Profile photos are not requested."},
		{ID: "work_extended", Name: "Chat management", Description: "Chat.ReadWrite, Chat.Create and User.ReadBasic.All for edits, deletion, reactions, read state and new chats."},
		{ID: "work_files", Name: "Chat management and files", Description: "Also requests Files.ReadWrite for OneDrive uploads and attachment downloads."},
		{ID: "work_channels", Name: "Chat and channel management with files", Description: "Also requests Channel.ReadBasic.All, ChannelMessage.Read.All, ChannelMessage.Send, ChannelMessage.ReadWrite and Files.ReadWrite.All."},
		{ID: "work_photos", Name: "Messaging and profile photos", Description: "Also requires approved User.ReadBasic.All."},
		{ID: "work_readonly", Name: "Read-only chats", Description: "Requires Chat.Read, without sending permission."},
		{ID: "work_device_code", Name: "Messaging — device code", Description: "Public-client registration; your organization must allow device-code sign-in."},
	}
}
func (c *Connector) CreateLogin(ctx context.Context, u *bridgev2.User, flow string) (bridgev2.LoginProcess, error) {
	profile := "messaging"
	switch flow {
	case "work_browser", "work_device_code":
	case "work_extended", "work_files", "work_channels":
		profile = strings.TrimPrefix(flow, "work_")
	case "work_photos":
		profile = "photos"
	case "work_readonly":
		profile = "readonly"
	default:
		return nil, bridgev2.ErrInvalidLoginFlowID
	}
	return &Login{main: c, user: u, browserFlow: flow != "work_device_code", settings: LoginSettings{Profile: profile, ClientID: c.Config.ClientID, TenantID: c.Config.TenantID}}, nil
}

type Login struct {
	settings      LoginSettings
	configured    bool
	awaitSecret   bool
	createdSecret bool
	completed     bool
	browserFlow   bool
	browser       *graph.BrowserLogin
	main          *Connector
	user          *bridgev2.User
	challenge     *graph.Challenge
	mu            sync.Mutex
	cancel        context.CancelFunc
	canceled      bool
}

func (l *Login) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
	if !l.configured {
		return l.inputStep(), nil
	}
	if l.browserFlow {
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.canceled {
			return nil, context.Canceled
		}
		b, err := l.settings.oauth().StartBrowser()
		if err != nil {
			return nil, err
		}
		l.browser = b
		return &bridgev2.LoginStep{Type: bridgev2.LoginStepTypeDisplayAndWait, StepID: "teamswork.browser", Instructions: "Open this link in a browser on the Mac running the bridge. Sign in with your work account, then return here. The link expires in 10 minutes.\n\n" + b.URL, DisplayAndWaitParams: &bridgev2.LoginDisplayAndWaitParams{Type: bridgev2.LoginDisplayTypeNothing}}, nil
	}

	c, err := l.settings.oauth().Start(ctx)
	if err != nil {
		return nil, err
	}
	l.challenge = c
	return &bridgev2.LoginStep{Type: bridgev2.LoginStepTypeDisplayAndWait, StepID: "teamswork.device_code", Instructions: fmt.Sprintf("Open %s and sign in with your work account. This code expires in %d minutes.", c.VerificationURI, c.ExpiresIn/60), DisplayAndWaitParams: &bridgev2.LoginDisplayAndWaitParams{Type: bridgev2.LoginDisplayTypeCode, Data: c.UserCode}}, nil
}
func (l *Login) Cancel() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.canceled = true
	if l.createdSecret && !l.completed {
		_ = os.Remove(l.settings.SecretFile)
	}
	if l.browser != nil {
		l.browser.Close()
	}
	if l.cancel != nil {
		l.cancel()
	}
}
func (l *Login) Wait(ctx context.Context) (*bridgev2.LoginStep, error) {
	if l.challenge == nil && l.browser == nil {
		return nil, fmt.Errorf("login has not started")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	l.mu.Lock()
	l.cancel = cancel
	if l.canceled {
		cancel()
	}
	l.mu.Unlock()
	var t *graph.Token
	var err error
	if l.browserFlow {
		t, err = l.settings.oauth().WaitBrowser(ctx, l.browser)
		if err != nil {
			return nil, fmt.Errorf("browser token exchange: %w", err)
		}
	} else {
		t, err = l.settings.oauth().Poll(ctx, l.challenge)
		if err != nil {
			return nil, fmt.Errorf("device-code token exchange: %w", err)
		}
	}
	if err != nil {
		return nil, err
	}
	api := &graph.Client{Token: func(context.Context) (string, error) { return t.Access, nil }}
	var me graph.User
	if err = api.Do(ctx, "GET", "/me", nil, &me); err != nil {
		return nil, err
	}
	if me.ID == "" {
		return nil, fmt.Errorf("Microsoft profile has no user ID")
	}
	// Tenant-scoped identities prevent accidental collisions across organizations.
	login, err := l.user.NewLogin(ctx, &database.UserLogin{ID: networkid.UserLoginID(l.settings.TenantID + ":" + me.ID), RemoteName: me.DisplayName, Metadata: &Metadata{Token: *t, UserID: me.ID, Auth: &l.settings}}, &bridgev2.NewLoginParams{DeleteOnConflict: true})
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	l.completed = true
	l.mu.Unlock()
	go login.Client.Connect(context.Background())
	return &bridgev2.LoginStep{Type: bridgev2.LoginStepTypeComplete, StepID: "teamswork.complete", Instructions: "Signed in as " + me.DisplayName, CompleteParams: &bridgev2.LoginCompleteParams{UserLoginID: login.ID, UserLogin: login}}, nil
}

var _ bridgev2.LoginProcessDisplayAndWait = (*Login)(nil)
