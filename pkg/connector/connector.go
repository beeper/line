package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
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
	Config          Config
	br              *bridgev2.Bridge
	loginFinalizeMu sync.Mutex
	directMedia     atomic.Bool
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
	return 1, 4
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
	const base = "qr_login: false\n"
	return base, &lc.Config, &configupgrade.StructUpgrader{
		Base: base,
		SimpleUpgrader: func(helper configupgrade.Helper) {
			helper.Copy(configupgrade.Bool, "qr_login")
		},
	}
}

type Config struct {
	QRLogin bool `yaml:"qr_login"`
}

func (lc *LineConnector) GetDBMetaTypes() database.MetaTypes {
	return database.MetaTypes{
		Portal: nil,
		Ghost:  nil,
		Message: func() any {
			return &MessageMetadata{}
		},
		Reaction: func() any {
			return &ReactionMetadata{}
		},
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
	client := &LineClient{
		UserLogin:          login,
		AccessToken:        accessToken,
		RefreshToken:       meta.RefreshToken,
		Mid:                meta.Mid,
		HTTPClient:         &http.Client{Timeout: 10 * time.Second},
		sessionInvalidated: meta.SessionInvalidated,
	}
	if err := client.loadChatDeletions(ctx); err != nil {
		return fmt.Errorf("failed to load chat deletions: %w", err)
	}
	login.Client = client
	return nil
}

const LoginFlowIDEmail = "dev.highest.matrix.line.email_login"
const LoginFlowIDQR = "dev.highest.matrix.line.qr_login"

func (lc *LineConnector) GetLoginFlows() []bridgev2.LoginFlow {
	flows := []bridgev2.LoginFlow{{
		Name:        "Login",
		Description: "Login with your LINE Email and Password",
		ID:          LoginFlowIDEmail,
	}}
	if lc.Config.QRLogin {
		flows = append([]bridgev2.LoginFlow{{Name: "QR Code", Description: "Scan a QR code with the LINE mobile app", ID: LoginFlowIDQR}}, flows...)
	}
	return flows
}

func (lc *LineConnector) CreateLogin(ctx context.Context, user *bridgev2.User, flowID string) (bridgev2.LoginProcess, error) {
	if flowID == LoginFlowIDQR && lc.Config.QRLogin {
		return &LineQRLogin{login: &LineEmailLogin{User: user, finalizeMu: &lc.loginFinalizeMu}}, nil
	}
	if flowID != LoginFlowIDEmail {
		return nil, bridgev2.ErrInvalidLoginFlowID
	}
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

	pollResult    chan *line.LoginResult
	pollErr       chan error
	polling       bool
	attempt       *line.LoginAttempt
	attemptCtx    context.Context
	attemptCancel context.CancelFunc
	canceled      bool
	mu            sync.Mutex
	finalizeMu    *sync.Mutex
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

	res, err := ll.loginCredentials(ctx, ll.Certificate)
	if err != nil {
		ll.logLoginFailure(err, "reconnect")
		reason := loginErrorReason(err)
		if reason == "" {
			reason = genericLoginFailureReason
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

	res, err := ll.loginCredentials(ctx, "")
	if err != nil {
		ll.logLoginFailure(err, "credentials")
		reason := loginErrorReason(err)
		if reason == "" {
			reason = genericLoginFailureReason
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

type loginErrorDetails struct {
	HTTPStatus        int
	ResponseCode      int
	ResponseMessage   string
	ErrorName         string
	ErrorCode         int
	ErrorMessage      string
	ErrorReason       string
	HasHTTPStatus     bool
	HasResponseCode   bool
	HasErrorCode      bool
	HasResponseFields bool
}

func parseLoginErrorDetails(err error) loginErrorDetails {
	var details loginErrorDetails
	if err == nil {
		return details
	}

	msg := err.Error()
	if apiErrorIndex := strings.Index(msg, "API error "); apiErrorIndex >= 0 {
		if parsed, scanErr := fmt.Sscanf(msg[apiErrorIndex:], "API error %d:", &details.HTTPStatus); parsed == 1 && scanErr == nil {
			details.HasHTTPStatus = true
		}
	}

	start := strings.Index(msg, "{")
	end := strings.LastIndex(msg, "}")
	if start == -1 || end == -1 || end <= start {
		return details
	}

	var payload struct {
		Code    *int            `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if json.Unmarshal([]byte(msg[start:end+1]), &payload) != nil {
		return details
	}

	details.HasResponseFields = true
	details.ResponseMessage = payload.Message
	if payload.Code != nil {
		details.ResponseCode = *payload.Code
		details.HasResponseCode = true
	}

	var responseError struct {
		Name    string `json:"name"`
		Code    *int   `json:"code"`
		Message string `json:"message"`
		Reason  string `json:"reason"`
	}
	if len(payload.Data) > 0 && json.Unmarshal(payload.Data, &responseError) == nil {
		details.ErrorName = responseError.Name
		details.ErrorMessage = responseError.Message
		details.ErrorReason = responseError.Reason
		if responseError.Code != nil {
			details.ErrorCode = *responseError.Code
			details.HasErrorCode = true
		}
	}
	return details
}

func loginErrorSummary(err error, details loginErrorDetails) string {
	if err == nil {
		return ""
	}
	if details.HasHTTPStatus {
		return fmt.Sprintf("API error %d", details.HTTPStatus)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "request timed out"
	}
	if errors.Is(err, context.Canceled) {
		return "request canceled"
	}
	return ""
}

func loginLogField(value string) string {
	value = strings.TrimSpace(value)
	const maxRunes = 512
	runes := []rune(value)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "…"
	}
	return value
}

func (ll *LineEmailLogin) logLoginFailure(err error, flow string) {
	if err == nil || ll.User == nil {
		return
	}

	details := parseLoginErrorDetails(err)
	event := ll.User.Log.Warn().
		Str("login_flow", flow).
		Bool("has_certificate", ll.Certificate != "")
	if summary := loginErrorSummary(err, details); summary != "" {
		event.Str("error_summary", summary)
	}
	if details.HasHTTPStatus {
		event.Int("http_status", details.HTTPStatus)
	}
	if details.HasResponseCode {
		event.Int("line_response_code", details.ResponseCode)
	}
	if details.ResponseMessage != "" {
		event.Str("line_response_message", loginLogField(details.ResponseMessage))
	}
	if details.ErrorName != "" {
		event.Str("line_error_name", loginLogField(details.ErrorName))
	}
	if details.HasErrorCode {
		event.Int("line_error_code", details.ErrorCode)
	}
	if details.ErrorMessage != "" {
		event.Str("line_error_message", loginLogField(details.ErrorMessage))
	}
	if details.ErrorReason != "" {
		event.Str("line_error_reason", loginLogField(details.ErrorReason))
	}
	event.Msg("LINE login attempt failed")
}

func loginErrorReason(err error) string {
	details := parseLoginErrorDetails(err)
	reason := details.ErrorReason
	if reason == "" {
		reason = details.ErrorMessage
	}
	if isBlockedUserLoginError(reason) {
		return loginTooManyAttemptsReason
	}
	return reason
}

func isBlockedUserLoginError(message string) bool {
	return strings.EqualFold(strings.TrimSpace(message), "blocked user")
}

func (ll *LineEmailLogin) loginCredentials(ctx context.Context, certificate string) (*line.LoginResult, error) {
	ll.mu.Lock()
	if ll.canceled {
		ll.mu.Unlock()
		return nil, context.Canceled
	}
	if ll.attemptCancel != nil {
		ll.attemptCancel()
	}
	if ll.attempt != nil {
		ll.attempt.Close()
	}
	ll.attemptCtx, ll.attemptCancel = context.WithCancel(context.WithoutCancel(ctx))
	attemptCtx := ll.attemptCtx
	attemptCancel := ll.attemptCancel
	ll.mu.Unlock()
	stop := context.AfterFunc(ctx, attemptCancel)
	defer stop()
	res, err := loginWithCredentialsContext(attemptCtx, ll.Email, ll.Password, certificate)
	ll.mu.Lock()
	defer ll.mu.Unlock()
	if ll.canceled || ctx.Err() != nil || attemptCtx.Err() != nil || ll.attemptCtx != attemptCtx {
		if res != nil {
			res.Attempt.Close()
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, context.Canceled
	}
	if res != nil {
		ll.attempt = res.Attempt
	}
	return res, err
}

func (ll *LineEmailLogin) Wait(ctx context.Context) (*bridgev2.LoginStep, error) {
	ll.mu.Lock()
	verifier, awaitingPIN := ll.Verifier, ll.AwaitingPIN
	resultCh, errCh := ll.pollResult, ll.pollErr
	var done <-chan struct{}
	if ll.attemptCtx != nil {
		done = ll.attemptCtx.Done()
	}
	canceled := ll.canceled
	ll.mu.Unlock()
	if canceled {
		return nil, context.Canceled
	}
	if verifier != "" {
		select {
		case res := <-resultCh:
			if res.AuthToken != "" {
				return ll.finishLogin(ctx, res)
			}
			res.Attempt.Close()
			return nil, ErrLoginVerificationFailed
		case err := <-errCh:
			ll.logLoginFailure(err, "verification_poll")
			return nil, wrapLineLoginError(err)
		case <-done:
			return nil, context.Canceled
		case <-ctx.Done():
			ll.Cancel()
			return nil, ctx.Err()
		}
	}

	if awaitingPIN {
		res, err := ll.loginCredentials(ctx, ll.Certificate)
		if err != nil {
			ll.logLoginFailure(err, "pin_continuation")
			return nil, wrapLineLoginError(err)
		}
		return ll.handleLoginResponse(ctx, res)
	}

	return nil, fmt.Errorf("no pending login continuation")
}

func (ll *LineEmailLogin) handleLoginResponse(ctx context.Context, res *line.LoginResult) (*bridgev2.LoginStep, error) {
	ll.mu.Lock()
	canceled := ll.canceled || res.Attempt != ll.attempt || !validLoginProducer(res)
	ll.mu.Unlock()
	if canceled || ctx.Err() != nil || (res.Attempt != nil && res.Attempt.Context.Err() != nil) {
		res.Attempt.Close()
		return nil, context.Canceled
	}

	if res.AuthToken != "" {
		return ll.finishLogin(ctx, res)
	}

	if (res.Type == 3 || res.Type == 0) && res.Verifier != "" {
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
		if ll.canceled {
			ll.mu.Unlock()
			res.Attempt.Close()
			return nil, context.Canceled
		}
		ll.Verifier, ll.NoE2EE, ll.AwaitingPIN = res.Verifier, res.NoE2EE, false
		ll.polling = true
		ll.pollResult = make(chan *line.LoginResult, 1)
		ll.pollErr = make(chan error, 1)
		attempt := res.Attempt
		verifier, noE2EE := ll.Verifier, ll.NoE2EE
		resultCh, errCh := ll.pollResult, ll.pollErr
		pollCtx := ll.attemptCtx
		if pollCtx == nil {
			pollCtx = ctx
		}
		go func() {
			client := newLineAPIClient("")
			client.LoginAttempt = attempt
			result, err := client.WaitForLogin(verifier, noE2EE)
			if pollCtx.Err() != nil {
				if attempt != nil {
					attempt.Close()
				}
				return
			}
			if err != nil {
				if attempt != nil {
					attempt.Close()
				}
				select {
				case errCh <- err:
				case <-pollCtx.Done():
				}
			} else {
				select {
				case resultCh <- result:
				case <-pollCtx.Done():
					if attempt != nil {
						attempt.Close()
					}
				}
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
		ll.mu.Lock()
		ll.AwaitingPIN = true
		ll.mu.Unlock()
		res.Attempt.Close()
		return &bridgev2.LoginStep{
			Type:         bridgev2.LoginStepTypeDisplayAndWait,
			StepID:       "dev.highest.matrix.line.enter_pin",
			Instructions: fmt.Sprintf("Please open the LINE app on your mobile device and enter this PIN code: **%s**\n\nAfter entering the code, click Continue below.", res.Certificate),
			DisplayAndWaitParams: &bridgev2.LoginDisplayAndWaitParams{
				Type: bridgev2.LoginDisplayTypeNothing,
			},
		}, nil
	}

	res.Attempt.Close()
	return nil, fmt.Errorf("login incomplete but no PIN found in response (Type: %d, Msg: %s)", res.Type, res.Message)
}

func (ll *LineEmailLogin) finishLogin(ctx context.Context, res *line.LoginResult) (*bridgev2.LoginStep, error) {
	if res != nil {
		defer res.Attempt.Close()
	}

	if res == nil {
		return nil, fmt.Errorf("login result missing")
	}

	ll.mu.Lock()
	processCtx := ll.attemptCtx
	canceled := ll.canceled || res.Attempt != ll.attempt || !validLoginProducer(res)
	if res.Attempt != nil {
		processCtx = res.Attempt.Context
	}
	ll.mu.Unlock()
	if canceled || ctx.Err() != nil || (processCtx != nil && processCtx.Err() != nil) {
		return nil, context.Canceled
	}
	finishCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if processCtx != nil {
		stop := context.AfterFunc(processCtx, cancel)
		defer stop()
	}
	ctx = finishCtx

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
	client.LoginAttempt = res.Attempt
	profile, err := client.GetProfileContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to verify token: %w", err)
	}

	displayName := profile.DisplayName
	if displayName == "" {
		displayName = "LINE User"
	}

	mid := profile.Mid
	if mid == "" || (res.Mid != "" && res.Mid != mid) {
		return nil, errors.New("login result does not match verified LINE account")
	}
	sameAccount := ll.ExistingMetadata != nil && ll.ExistingLogin != nil && ll.ExistingLogin.UserLogin != nil && mid == string(ll.ExistingLogin.ID) && mid == ll.ExistingMetadata.Mid
	certificate := res.Certificate
	if certificate == "" && (ll.ExistingMetadata == nil || sameAccount) {
		certificate = ll.Certificate
	}
	meta := &UserLoginMetadata{AccessToken: token, RefreshToken: refreshToken, Email: ll.Email, Password: ll.Password, Certificate: certificate, Mid: mid}
	if sameAccount && meta.Email == "" && meta.Password == "" {
		meta.Email, meta.Password = ll.ExistingMetadata.Email, ll.ExistingMetadata.Password
	}

	loginManager, err := ll.fetchLoginKeys(res, meta, client)
	if err != nil {
		if isTerminalCryptoError(err) || errors.Is(err, context.Canceled) || !sameAccount || !shouldPreserveExistingE2EEKeys(false, ll.ExistingMetadata) {
			return nil, err
		}
		if err := ll.admitLogin(ctx, res); err != nil {
			return nil, err
		}
		ll.User.Bridge.Log.Warn().Err(err).Msg("Login: failed to export E2EE keys; using existing keys")
	}
	exportedKeys := loginManager != nil
	if loginManager != nil {
		defer loginManager.Close()
	}
	if sameAccount && shouldPreserveExistingE2EEKeys(exportedKeys, ll.ExistingMetadata) {
		copyLoginE2EEKeyMetadata(meta, ll.ExistingMetadata)
		ll.User.Bridge.Log.Info().Int("keys", len(meta.ExportedKeyMap)).Msg("Preserved existing E2EE keys after re-login")
	}
	if exportedKeys && sameAccount {
		meta.ExportedKeyMap = mergeLoginKeyMaps(ll.ExistingMetadata.ExportedKeyMap, meta.ExportedKeyMap)
	}
	if !res.NoE2EE && len(meta.ExportedKeyMap) == 0 {
		return nil, ErrLoginNoKeychain
	}

	detectedLineID := networkid.UserLoginID(mid)
	// bridgev2 reuses same-ID UserLogin objects and replaces their metadata
	// before replacing Client. Serialize that handoff so concurrent login
	// completions cannot orphan each other's clients.
	if ll.finalizeMu != nil {
		ll.finalizeMu.Lock()
		defer ll.finalizeMu.Unlock()
	}
	if err := ll.admitLogin(ctx, res); err != nil {
		return nil, err
	}
	targetLogin := findReusedLineLogin(ll.ExistingLogin, ll.User.GetUserLogins(), detectedLineID)
	if err := ll.admitLogin(ctx, res); err != nil {
		return nil, err
	}
	retireLineLogin(targetLogin)

	var newClient *LineClient
	ul, err := ll.User.NewLogin(ctx, &database.UserLogin{
		ID:         detectedLineID,
		RemoteName: displayName,
		Metadata:   meta,
	}, &bridgev2.NewLoginParams{
		LoadUserLogin: func(_ context.Context, login *bridgev2.UserLogin) error {
			if err := ll.admitLogin(ctx, res); err != nil {
				return err
			}
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
	if err := ll.admitLogin(ctx, res); err != nil {
		newClient.Disconnect()
		return nil, err
	}
	if ll.ExistingLogin != nil && ll.ExistingLogin.UserLogin != nil && ll.ExistingLogin.ID != ul.ID {
		// A different-ID override remains valid until the new login is safely
		// installed. Retire it only after NewLogin succeeds.
		retireLineLogin(ll.ExistingLogin)
	}

	if loginManager != nil {
		if err := loginManager.SaveSecureDataToFile(loginSecureDataID(meta, string(ll.User.MXID)), map[string]any{"exportedKeyMap": meta.ExportedKeyMap}); err != nil {
			ll.User.Bridge.Log.Warn().Err(err).Msg("Login: failed to save E2EE secure data")
		}
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
	var mgr *e2ee.Manager
	if res.Attempt == nil {
		unused, err := newE2EEManager()
		if err != nil {
			return nil, nil, fmt.Errorf("create E2EE manager: %w", err)
		}
		unused.Close()
		return nil, nil, errors.New("missing E2EE login attempt")
	}
	mgr = e2ee.NewManagerWithRunner(res.Attempt.Runner)
	success := false
	defer func() {
		if !success {
			mgr.Close()
		}
	}()
	client.LoginAttempt = res.Attempt
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
	success = true
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

func mergeLoginKeyMaps(previous, current map[string]string) map[string]string {
	merged := make(map[string]string, len(previous)+len(current))
	for id, key := range previous {
		merged[id] = key
	}
	for id, key := range current {
		merged[id] = key
	}
	return merged
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

func (ll *LineEmailLogin) fetchLoginKeys(res *line.LoginResult, meta *UserLoginMetadata, client *line.Client) (*e2ee.Manager, error) {
	if res.EncryptedKeyChain == "" || res.E2EEPublicKey == "" {
		return nil, nil
	}
	mgr, exported, err := exportLoginE2EEKeys(res, client)
	if err != nil {
		return nil, fmt.Errorf("export login E2EE keys: %w", err)
	}
	applyExportedLoginE2EEKeys(meta, res, exported)

	ll.User.Bridge.Log.Info().Int("keys", len(exported)).Msg("Login: E2EE keys exported successfully")
	return mgr, nil
}

func (ll *LineEmailLogin) admitLogin(ctx context.Context, res *line.LoginResult) error {
	ll.mu.Lock()
	defer ll.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if ll.canceled || res.Attempt != ll.attempt || !validLoginProducer(res) {
		return context.Canceled
	}
	if res.Attempt != nil {
		return res.Attempt.Context.Err()
	}
	if ll.attemptCtx != nil {
		return ll.attemptCtx.Err()
	}
	return nil
}

func (ll *LineEmailLogin) Cancel() {
	ll.mu.Lock()
	ll.canceled = true
	if ll.attemptCancel != nil {
		ll.attemptCancel()
	}
	attempt := ll.attempt
	ll.mu.Unlock()
	if attempt != nil {
		attempt.Close()
	}
}
