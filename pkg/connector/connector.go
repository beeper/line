package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.mau.fi/util/configupgrade"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/status"

	"github.com/highesttt/matrix-line-messenger/pkg/e2ee"
	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

const (
	loginTooManyAttemptsReason       = "Too many login attempts"
	loginTooManyAttemptsInstructions = "Too many login attempts. LINE has locked login for this account temporarily."
)

type LineConnector struct {
	br              *bridgev2.Bridge
	loginFinalizeMu sync.Mutex
}

var _ bridgev2.NetworkConnector = (*LineConnector)(nil)

func (lc *LineConnector) Init(bridge *bridgev2.Bridge) {
	// Keep connection state in Beeper's bridge status API only. Framework status
	// notices call GetManagementRoom, which creates the LINE bot room when the
	// user doesn't already have one.
	bridge.Config.BridgeStatusNotices = "none"
	lc.br = bridge
}

func (lc *LineConnector) Start(ctx context.Context) error {
	_, ok := lc.br.Matrix.(bridgev2.MatrixConnectorWithServer)
	if !ok {
		return fmt.Errorf("matrix connector does not implement MatrixConnectorWithServer")
	}
	return nil
}

func (lc *LineConnector) GetBridgeInfoVersion() (info, capabilities int) {
	return 1, 2
}

func (lc *LineConnector) GetCapabilities() *bridgev2.NetworkGeneralCapabilities {
	return &bridgev2.NetworkGeneralCapabilities{
		AggressiveUpdateInfo: true,
		Provisioning: bridgev2.ProvisioningCapabilities{
			ImagePackImport: false,
			ResolveIdentifier: bridgev2.ResolveIdentifierCapabilities{
				Search:      true,
				ContactList: true,
				CreateDM:    true,
			},
			GroupCreation: map[string]bridgev2.GroupTypeCapabilities{
				"group": {
					TypeDescription: "LINE Group",
					Name: bridgev2.GroupFieldCapability{
						Allowed:   true,
						MinLength: 1,
						MaxLength: 50,
					},
					Participants: bridgev2.GroupFieldCapability{
						Allowed:   true,
						MinLength: 1,
						MaxLength: 100,
					},
				},
			},
		},
	}
}

func (lc *LineConnector) GetName() bridgev2.BridgeName {
	return bridgev2.BridgeName{
		DisplayName:      "LINE",
		NetworkURL:       "https://line.me",
		NetworkIcon:      "",
		NetworkID:        "line",
		BeeperBridgeType: "github.com/highesttt/matrix-line-messenger",
		DefaultPort:      29322,
	}
}

func (lc *LineConnector) GetConfig() (example string, data any, upgrader configupgrade.Upgrader) {
	return "", nil, nil
}

func (lc *LineConnector) GetDBMetaTypes() database.MetaTypes {
	return database.MetaTypes{
		Portal:   nil,
		Ghost:    nil,
		Message:  nil,
		Reaction: nil,
		UserLogin: func() any {
			return &UserLoginMetadata{}
		},
	}
}

type UserLoginMetadata struct {
	AccessToken        string            `json:"access_token"`
	RefreshToken       string            `json:"refresh_token,omitempty"`
	SessionInvalidated bool              `json:"session_invalidated,omitempty"`
	Email              string            `json:"email,omitempty"`
	Password           string            `json:"password,omitempty"`
	Certificate        string            `json:"certificate,omitempty"`
	Mid                string            `json:"mid,omitempty"`
	EncryptedKeyChain  string            `json:"encrypted_key_chain,omitempty"`
	E2EEPublicKey      string            `json:"e2ee_public_key,omitempty"`
	E2EEVersion        string            `json:"e2ee_version,omitempty"`
	E2EEKeyID          string            `json:"e2ee_key_id,omitempty"`
	ExportedKeyMap     map[string]string `json:"exported_key_map,omitempty"`
	ForceFullE2EELogin bool              `json:"force_full_e2ee_login,omitempty"`
	BlockedMIDs        []string          `json:"blocked_mids,omitempty"`
}

func (lc *LineConnector) LoadUserLogin(ctx context.Context, login *bridgev2.UserLogin) error {
	meta := login.Metadata.(*UserLoginMetadata)
	accessToken := meta.AccessToken
	if meta.SessionInvalidated {
		accessToken = ""
	}
	login.Client = &LineClient{
		UserLogin:          login,
		AccessToken:        accessToken,
		RefreshToken:       meta.RefreshToken,
		Mid:                meta.Mid,
		HTTPClient:         &http.Client{Timeout: 10 * time.Second},
		sessionInvalidated: meta.SessionInvalidated,
	}
	return nil
}

func (lc *LineConnector) GetLoginFlows() []bridgev2.LoginFlow {
	return []bridgev2.LoginFlow{{
		Name:        "Login",
		Description: "Login with your LINE Email and Password",
		ID:          "dev.highest.matrix.line.email_login",
	}}
}

func (lc *LineConnector) CreateLogin(ctx context.Context, user *bridgev2.User, flowID string) (bridgev2.LoginProcess, error) {
	return &LineEmailLogin{User: user, finalizeMu: &lc.loginFinalizeMu}, nil
}

type LineEmailLogin struct {
	User        *bridgev2.User
	Email       string
	Password    string
	Certificate string
	Verifier    string
	AwaitingPIN bool
	NoE2EE      bool // True when login fell back to non-E2EE (LSOFF account)

	ExistingMetadata *UserLoginMetadata
	ExistingLogin    *bridgev2.UserLogin

	pollResult chan *line.LoginResult
	pollErr    chan error
	polling    bool
	mu         sync.Mutex
	finalizeMu *sync.Mutex
}

var _ bridgev2.LoginProcessUserInput = (*LineEmailLogin)(nil)
var _ bridgev2.LoginProcessDisplayAndWait = (*LineEmailLogin)(nil)
var _ bridgev2.LoginProcessWithOverride = (*LineEmailLogin)(nil)

func (ll *LineEmailLogin) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeUserInput,
		StepID:       "dev.highest.matrix.line.enter_creds",
		Instructions: "Please enter your LINE email and password.",
		UserInputParams: &bridgev2.LoginUserInputParams{
			Fields: []bridgev2.LoginInputDataField{
				{
					Type: bridgev2.LoginInputFieldTypeUsername,
					ID:   "email",
					Name: "Email",
				},
				{
					Type: bridgev2.LoginInputFieldTypePassword,
					ID:   "password",
					Name: "Password",
				},
			},
		},
	}, nil
}

func (ll *LineEmailLogin) StartWithOverride(ctx context.Context, override *bridgev2.UserLogin) (*bridgev2.LoginStep, error) {
	meta, ok := override.Metadata.(*UserLoginMetadata)
	if !ok {
		return nil, fmt.Errorf("existing LINE login metadata has unexpected type %T", override.Metadata)
	}
	ll.Email = meta.Email
	ll.Password = meta.Password
	ll.Certificate = meta.Certificate
	ll.ExistingMetadata = meta
	ll.ExistingLogin = override

	if ll.Email == "" || ll.Password == "" {
		return ll.loginErrorStep("No stored LINE credentials are available. Please enter your LINE email and password to reconnect."), nil
	}
	if meta.ForceFullE2EELogin || len(meta.ExportedKeyMap) == 0 {
		ll.Certificate = ""
		if override.Bridge != nil {
			override.Bridge.Log.Info().
				Bool("missing_e2ee_key", meta.ForceFullE2EELogin).
				Bool("has_exported_keys", len(meta.ExportedKeyMap) > 0).
				Msg("Forcing full LINE reconnect to refresh E2EE keys")
		}
	}

	override.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnecting})

	res, err := loginWithCredentials(ll.Email, ll.Password, ll.Certificate)
	if err != nil {
		reason := loginErrorReason(err)
		if reason == "" {
			reason = fmt.Sprintf("Login failed: %v", err)
		}
		return ll.loginErrorStep(reason), nil
	}
	return ll.handleLoginResponse(ctx, res)
}

func (ll *LineEmailLogin) SubmitUserInput(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	if ll.Verifier != "" {
		return ll.Wait(ctx)
	}

	if input["email"] != "" {
		ll.Email = input["email"]
		ll.Password = input["password"]
		ll.Certificate = ""
	}

	if ll.Email == "" || ll.Password == "" {
		return ll.loginErrorStep("Email and password are required"), nil
	}

	res, err := loginWithCredentials(ll.Email, ll.Password, "")
	if err != nil {
		reason := loginErrorReason(err)
		if reason == "" {
			reason = fmt.Sprintf("Login failed: %v", err)
		}
		return ll.loginErrorStep(reason), nil
	}

	return ll.handleLoginResponse(ctx, res)
}

func (ll *LineEmailLogin) loginErrorStep(message string) *bridgev2.LoginStep {
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeUserInput,
		StepID:       "dev.highest.matrix.line.enter_creds",
		Instructions: loginErrorInstructions(message),
		UserInputParams: &bridgev2.LoginUserInputParams{
			Fields: []bridgev2.LoginInputDataField{
				{
					Type: bridgev2.LoginInputFieldTypeUsername,
					ID:   "email",
					Name: "Email",
				},
				{
					Type: bridgev2.LoginInputFieldTypePassword,
					ID:   "password",
					Name: "Password",
				},
			},
		},
	}
}

func loginErrorInstructions(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return "Could not log in to LINE. Please check your email and password and try again."
	}
	if strings.EqualFold(message, loginTooManyAttemptsReason) || isBlockedUserLoginError(message) {
		return loginTooManyAttemptsInstructions
	}
	if strings.EqualFold(message, "Account ID or password is invalid") {
		return "LINE rejected the email or password. Make sure you used the email from LINE Settings -> Account -> Email Address, then try again."
	}
	return fmt.Sprintf("Could not log in to LINE: %s", message)
}

func loginErrorReason(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	start := strings.Index(msg, "{")
	end := strings.LastIndex(msg, "}")
	if start == -1 || end == -1 || end <= start {
		return ""
	}
	var payload struct {
		Data struct {
			Message string `json:"message"`
			Reason  string `json:"reason"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(msg[start:end+1]), &payload); err != nil {
		return ""
	}
	reason := payload.Data.Reason
	if reason == "" {
		reason = payload.Data.Message
	}
	if isBlockedUserLoginError(reason) {
		return loginTooManyAttemptsReason
	}
	return reason
}

func isBlockedUserLoginError(message string) bool {
	return strings.EqualFold(strings.TrimSpace(message), "blocked user")
}

func (ll *LineEmailLogin) Wait(ctx context.Context) (*bridgev2.LoginStep, error) {
	if ll.Verifier != "" {
		select {
		case res := <-ll.pollResult:
			if res.AuthToken != "" {
				return ll.finishLogin(ctx, res)
			}
			return nil, fmt.Errorf("verification failed: no auth token received")
		case err := <-ll.pollErr:
			return nil, fmt.Errorf("verification failed: %w", err)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	if ll.AwaitingPIN {
		res, err := loginWithCredentials(ll.Email, ll.Password, ll.Certificate)
		if err != nil {
			return nil, fmt.Errorf("login failed: %w", err)
		}
		return ll.handleLoginResponse(ctx, res)
	}

	return nil, fmt.Errorf("no pending login continuation")
}

func (ll *LineEmailLogin) handleLoginResponse(ctx context.Context, res *line.LoginResult) (*bridgev2.LoginStep, error) {
	if res.AuthToken != "" {
		return ll.finishLogin(ctx, res)
	}

	if (res.Type == 3 || res.Type == 0) && res.Verifier != "" {
		ll.Verifier = res.Verifier
		ll.NoE2EE = res.NoE2EE
		ll.AwaitingPIN = false
		instructions := "Please open the LINE app on your mobile device to complete the login."
		pin := res.Pin
		if res.PinCode != "" {
			pin = res.PinCode
		}
		if pin != "" {
			instructions = fmt.Sprintf("Open LINE and enter this PIN on your mobile device: %s", pin)
		}

		// Start polling in background immediately so it's running while the user enters the PIN
		ll.mu.Lock()
		ll.polling = true
		ll.pollResult = make(chan *line.LoginResult, 1)
		ll.pollErr = make(chan error, 1)
		go func() {
			client := newLineAPIClient("")
			res, err := client.WaitForLogin(ll.Verifier, ll.NoE2EE)
			if err != nil {
				ll.pollErr <- err
			} else {
				ll.pollResult <- res
			}
		}()
		ll.mu.Unlock()

		return &bridgev2.LoginStep{
			Type:         bridgev2.LoginStepTypeDisplayAndWait,
			StepID:       "dev.highest.matrix.line.wait_verification",
			Instructions: instructions,
			DisplayAndWaitParams: &bridgev2.LoginDisplayAndWaitParams{
				Type: bridgev2.LoginDisplayTypeNothing,
			},
		}, nil
	}

	if res.Certificate != "" {
		ll.AwaitingPIN = true
		return &bridgev2.LoginStep{
			Type:         bridgev2.LoginStepTypeDisplayAndWait,
			StepID:       "dev.highest.matrix.line.enter_pin",
			Instructions: fmt.Sprintf("Please open the LINE app on your mobile device and enter this PIN code: **%s**\n\nAfter entering the code, click Continue below.", res.Certificate),
			DisplayAndWaitParams: &bridgev2.LoginDisplayAndWaitParams{
				Type: bridgev2.LoginDisplayTypeNothing,
			},
		}, nil
	}

	return nil, fmt.Errorf("login incomplete but no PIN found in response (Type: %d, Msg: %s)", res.Type, res.Message)
}

func (ll *LineEmailLogin) finishLogin(ctx context.Context, res *line.LoginResult) (*bridgev2.LoginStep, error) {
	if res == nil {
		return nil, fmt.Errorf("login result missing")
	}

	token := res.AuthToken
	refreshToken := ""
	if res.TokenV3IssueResult != nil {
		if token == "" {
			token = res.TokenV3IssueResult.AccessToken
		}
		refreshToken = res.TokenV3IssueResult.RefreshToken
	}
	if token == "" {
		return nil, fmt.Errorf("missing access token in login result")
	}

	client := newLineAPIClient(token)
	profile, err := client.GetProfile()
	if err != nil {
		return nil, fmt.Errorf("failed to verify token: %w", err)
	}

	displayName := profile.DisplayName
	if displayName == "" {
		displayName = "LINE User"
	}

	certificate := res.Certificate
	if certificate == "" {
		certificate = ll.Certificate
	}
	mid := res.Mid
	if mid == "" {
		mid = profile.Mid
	}

	meta := &UserLoginMetadata{AccessToken: token, RefreshToken: refreshToken, Email: ll.Email, Password: ll.Password, Certificate: certificate, Mid: mid}

	exportedKeys := ll.fetchLoginKeys(res, meta, client)
	if shouldPreserveExistingE2EEKeys(exportedKeys, ll.ExistingMetadata) {
		copyLoginE2EEKeyMetadata(meta, ll.ExistingMetadata)
		ll.User.Bridge.Log.Info().Int("keys", len(meta.ExportedKeyMap)).Msg("Preserved existing E2EE keys after re-login")
	}
	if !res.NoE2EE && len(meta.ExportedKeyMap) == 0 {
		return nil, fmt.Errorf("LINE login completed without E2EE keychain; please reconnect again and complete the LINE verification prompt")
	}

	detectedLineID := networkid.UserLoginID(mid)
	// bridgev2 reuses same-ID UserLogin objects and replaces their metadata
	// before replacing Client. Serialize that handoff so concurrent login
	// completions cannot orphan each other's clients.
	if ll.finalizeMu != nil {
		ll.finalizeMu.Lock()
		defer ll.finalizeMu.Unlock()
	}
	targetLogin := findReusedLineLogin(ll.ExistingLogin, ll.User.GetUserLogins(), detectedLineID)
	retireLineLogin(targetLogin)

	var newClient *LineClient
	ul, err := ll.User.NewLogin(ctx, &database.UserLogin{
		ID:         detectedLineID,
		RemoteName: displayName,
		Metadata:   meta,
	}, &bridgev2.NewLoginParams{
		LoadUserLogin: func(ctx context.Context, login *bridgev2.UserLogin) error {
			newClient = &LineClient{
				UserLogin:    login,
				AccessToken:  token,
				RefreshToken: refreshToken,
				Mid:          mid,
				HTTPClient:   &http.Client{Timeout: 10 * time.Second},
			}
			login.Client = newClient
			return nil
		},
	})
	if err != nil {
		if newClient != nil {
			newClient.Disconnect()
		}
		return nil, fmt.Errorf("failed to create user login: %w", err)
	}
	if newClient == nil {
		return nil, fmt.Errorf("failed to create LINE client")
	}
	if ll.ExistingLogin != nil && ll.ExistingLogin.UserLogin != nil && ll.ExistingLogin.ID != ul.ID {
		// A different-ID override remains valid until the new login is safely
		// installed. Retire it only after NewLogin succeeds.
		retireLineLogin(ll.ExistingLogin)
	}

	go newClient.Connect(context.Background())

	return &bridgev2.LoginStep{
		Type:           bridgev2.LoginStepTypeComplete,
		StepID:         "dev.highest.matrix.line.complete",
		Instructions:   "Successfully logged in",
		CompleteParams: &bridgev2.LoginCompleteParams{UserLoginID: ul.ID, UserLogin: ul},
	}, nil
}

func retireLineLogin(login *bridgev2.UserLogin) {
	if login == nil {
		return
	}
	if client, ok := login.Client.(*LineClient); ok {
		client.retire()
	}
}

func findReusedLineLogin(
	override *bridgev2.UserLogin,
	current []*bridgev2.UserLogin,
	detectedID networkid.UserLoginID,
) *bridgev2.UserLogin {
	if override != nil && override.UserLogin != nil && override.ID == detectedID {
		return override
	}
	for _, login := range current {
		if login != nil && login.UserLogin != nil && login.ID == detectedID {
			return login
		}
	}
	return nil
}

func exportLoginE2EEKeys(res *line.LoginResult, client *line.Client) (*e2ee.Manager, map[string]string, error) {
	if res.EncryptedKeyChain == "" || res.E2EEPublicKey == "" {
		return nil, nil, nil
	}
	mgr, err := newE2EEManager()
	if err != nil {
		return nil, nil, fmt.Errorf("create E2EE manager: %w", err)
	}
	ei3, err := client.GetEncryptedIdentityV3()
	if err != nil {
		return nil, nil, fmt.Errorf("get EncryptedIdentityV3: %w", err)
	}
	if err := mgr.InitStorage(ei3.WrappedNonce, ei3.KDFParameter1, ei3.KDFParameter2); err != nil {
		return nil, nil, fmt.Errorf("init storage: %w", err)
	}
	exported, err := mgr.InitFromLoginKeyChain(res.E2EEPublicKey, res.EncryptedKeyChain)
	if err != nil {
		return nil, nil, fmt.Errorf("init from login keychain: %w", err)
	}
	return mgr, exported, nil
}

func saveLoginE2EEKeyMetadata(meta *UserLoginMetadata, res *line.LoginResult) {
	meta.EncryptedKeyChain = res.EncryptedKeyChain
	meta.E2EEPublicKey = res.E2EEPublicKey
	meta.E2EEVersion = res.E2EEVersion
	meta.E2EEKeyID = res.E2EEKeyID
}

func applyExportedLoginE2EEKeys(meta *UserLoginMetadata, res *line.LoginResult, exported map[string]string) {
	saveLoginE2EEKeyMetadata(meta, res)
	meta.ExportedKeyMap = exported
	meta.ForceFullE2EELogin = false
}

func copyLoginE2EEKeyMetadata(dst, src *UserLoginMetadata) {
	dst.EncryptedKeyChain = src.EncryptedKeyChain
	dst.E2EEPublicKey = src.E2EEPublicKey
	dst.E2EEVersion = src.E2EEVersion
	dst.E2EEKeyID = src.E2EEKeyID
	if len(src.ExportedKeyMap) > 0 {
		dst.ExportedKeyMap = make(map[string]string, len(src.ExportedKeyMap))
		for keyID, exported := range src.ExportedKeyMap {
			dst.ExportedKeyMap[keyID] = exported
		}
	}
}

func shouldPreserveExistingE2EEKeys(exportedKeys bool, existing *UserLoginMetadata) bool {
	return !exportedKeys && existing != nil && len(existing.ExportedKeyMap) > 0 && !existing.ForceFullE2EELogin
}

func loginSecureDataID(meta *UserLoginMetadata, fallback string) string {
	if meta.Mid != "" {
		return meta.Mid
	}
	return fallback
}

func (ll *LineEmailLogin) fetchLoginKeys(res *line.LoginResult, meta *UserLoginMetadata, client *line.Client) bool {
	if res.EncryptedKeyChain == "" || res.E2EEPublicKey == "" {
		return false
	}
	mgr, exported, err := exportLoginE2EEKeys(res, client)
	if err != nil {
		ll.User.Bridge.Log.Warn().Err(err).Msg("Login: failed to export E2EE keys")
		return false
	}
	applyExportedLoginE2EEKeys(meta, res, exported)
	if err := mgr.SaveSecureDataToFile(loginSecureDataID(meta, string(ll.User.MXID)), map[string]any{"exportedKeyMap": exported}); err != nil {
		ll.User.Bridge.Log.Warn().Err(err).Msg("Login: failed to save E2EE secure data")
	}
	ll.User.Bridge.Log.Info().Int("keys", len(exported)).Msg("Login: E2EE keys exported successfully")
	return true
}

func (ll *LineEmailLogin) Cancel() {}
