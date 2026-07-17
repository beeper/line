package connector

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/status"

	"github.com/highesttt/matrix-line-messenger/pkg/e2ee"
	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

var (
	errLineSessionInvalidated = errors.New("LINE session invalidated by another client")
	errLineClientSuperseded   = errors.New("LINE client was superseded")
)

const lineMissingE2EEKeyMessage = "LINE encryption keys are unavailable. Reconnect LINE in Beeper to restore message decryption."

var newLineAPIClient = line.NewClient
var newE2EEManager = e2ee.NewManager

var loginWithCredentials = func(email, password, certificate string) (*line.LoginResult, error) {
	return newLineAPIClient("").Login(email, password, certificate)
}

var getProfileWithToken = func(ctx context.Context, token string) (*line.Profile, error) {
	return newLineAPIClient(token).GetProfileContext(ctx)
}

type LineClient struct {
	UserLogin    *bridgev2.UserLogin
	AccessToken  string
	RefreshToken string
	Mid          string
	HTTPClient   *http.Client
	E2EE         *e2ee.Manager
	peerKeys     map[string]peerKeyInfo

	reqSeqMu    sync.Mutex
	sentReqSeqs map[int]time.Time
	lastReqSeq  int

	tokenMu              sync.RWMutex
	recoverMu            sync.Mutex
	recoverTime          time.Time
	recoveryStopped      bool
	missingE2EEKeyMu     sync.Mutex
	missingE2EEKeyMarked bool
	// sessionInvalidated is set when LINE forcefully logs out this Chrome-style
	// session. It prevents background calls from re-logging in before the user
	// clicks Reconnect.
	sessionInvalidated bool
	runMu              sync.Mutex
	activeRun          *lineClientRun
	stopped            bool
	superseded         atomic.Bool
	forcedLogoutMu     sync.Mutex
	forcedLogoutSent   bool

	// cacheMu protects peerKeys, blockedUsers, contactCache, mediaFlowCache,
	// noE2EEGroups, groupMemberCache, generatedGroupNameCache, and knownMemberChatMIDs.
	// Hold it only around map accesses; never across network calls.
	cacheMu                 sync.Mutex
	blockedUsers            map[string]bool      // mid -> true if the user has blocked this contact in LINE
	noE2EEGroups            map[string]time.Time // chatMid -> when group E2EE failure was cached
	contactCache            map[string]cachedContact
	mediaFlowCache          map[string]cachedMediaFlow
	groupMemberCache        map[string][]string // chatMid -> list of member MIDs from CreateGroup or getChatMemberMIDs
	generatedGroupNameCache map[string]bool     // chatMid -> true when Matrix name should be generated from member names
	knownMemberChatMIDs     map[string]struct{} // chatMid -> current member chats returned by getAllChatMids
	reactionIconMXC         map[int]string      // predefinedReactionType -> cached MXC URI
	recentReactions         sync.Map            // "msgID\x00emoji" -> struct{} to dedup concurrent 139/140 events

	wg sync.WaitGroup
}

type lineClientRun struct {
	cancel context.CancelFunc
}

type cachedMediaFlow struct {
	flowMap  map[string]int
	cachedAt time.Time
	ttl      time.Duration
}

type peerKeyInfo struct {
	raw       int
	pub       string
	noE2EE    bool      // true if peer has Letter Sealing off
	checkedAt time.Time // when noE2EE was last verified
}

const contactCacheTTL = 1 * time.Hour

type cachedContact struct {
	line.Contact
	cachedAt time.Time
}

const defaultMediaFlowTTL = 6 * time.Hour
const recentTokenRecoveryWindow = 10 * time.Second

func (lc *LineClient) getAccessToken() string {
	lc.tokenMu.RLock()
	defer lc.tokenMu.RUnlock()
	return lc.AccessToken
}

func (lc *LineClient) getTokens() (string, string) {
	lc.tokenMu.RLock()
	defer lc.tokenMu.RUnlock()
	return lc.AccessToken, lc.RefreshToken
}

func (lc *LineClient) setTokens(accessToken, refreshToken string) (string, string) {
	lc.tokenMu.Lock()
	defer lc.tokenMu.Unlock()
	lc.AccessToken = accessToken
	if accessToken != "" {
		lc.sessionInvalidated = false
	}
	if refreshToken != "" {
		lc.RefreshToken = refreshToken
	}
	return lc.AccessToken, lc.RefreshToken
}

func (lc *LineClient) invalidateAccessToken() {
	lc.tokenMu.Lock()
	lc.AccessToken = ""
	lc.sessionInvalidated = true
	lc.tokenMu.Unlock()
}

func (lc *LineClient) hasAccessToken() bool {
	return lc.getAccessToken() != ""
}

func (lc *LineClient) isSessionInvalidated() bool {
	lc.tokenMu.RLock()
	defer lc.tokenMu.RUnlock()
	return lc.sessionInvalidated
}

func (lc *LineClient) beginRun(parent context.Context) (context.Context, *lineClientRun, bool) {
	ctx, cancel := context.WithCancel(parent)
	run := &lineClientRun{cancel: cancel}
	lc.runMu.Lock()
	if lc.stopped {
		lc.runMu.Unlock()
		cancel()
		return ctx, run, false
	}
	// Reserve one count for the full Connect invocation before publishing the
	// run. Disconnect cannot observe zero and return while startup is in flight.
	lc.wg.Add(1)
	previous := lc.activeRun
	lc.activeRun = run
	lc.runMu.Unlock()
	if previous != nil {
		previous.cancel()
	}
	return ctx, run, true
}

func (lc *LineClient) cancelActiveRun() {
	lc.runMu.Lock()
	run := lc.activeRun
	lc.runMu.Unlock()
	if run != nil {
		run.cancel()
	}
}

func (lc *LineClient) retire() {
	lc.Disconnect()
}

func (lc *LineClient) claimForcedLogoutState() bool {
	lc.forcedLogoutMu.Lock()
	defer lc.forcedLogoutMu.Unlock()
	if lc.forcedLogoutSent {
		return false
	}
	lc.forcedLogoutSent = true
	return true
}

func (lc *LineClient) newClient() *line.Client {
	return newLineAPIClient(lc.getAccessToken())
}

func (lc *LineClient) avatarFromPicturePath(picturePath string) *bridgev2.Avatar {
	if picturePath == "" {
		return &bridgev2.Avatar{Remove: true}
	}
	return &bridgev2.Avatar{
		ID: networkid.AvatarID(picturePath),
		Get: func(ctx context.Context) ([]byte, error) {
			return lc.GetAvatar(ctx, networkid.AvatarID(picturePath))
		},
	}
}

// shouldUseE2EEMediaFlow checks whether the server wants E2EE upload (flow 2)
// for the given chat and content type. Returns true for E2EE, false for plain.
// Falls back to true (E2EE) if the server call fails, to preserve existing behavior.
func (lc *LineClient) shouldUseE2EEMediaFlow(chatMid string, contentType int) bool {
	lc.cacheMu.Lock()
	if lc.mediaFlowCache == nil {
		lc.mediaFlowCache = make(map[string]cachedMediaFlow)
	}
	if cached, ok := lc.mediaFlowCache[chatMid]; ok && time.Since(cached.cachedAt) < cached.ttl {
		lc.cacheMu.Unlock()
		flow, exists := cached.flowMap[strconv.Itoa(contentType)]
		if exists {
			return flow == 2
		}
		return true
	}
	lc.cacheMu.Unlock()

	_, resp, err := callLineResult(lc, context.Background(), func(client *line.Client) (*line.MediaMessageFlowResponse, error) {
		return client.DetermineMediaMessageFlow(chatMid)
	})
	if err != nil {
		lc.UserLogin.Bridge.Log.Warn().Err(err).Str("chat_mid", chatMid).
			Msg("Failed to determine media flow, defaulting to E2EE upload")
		return true
	}

	ttl := defaultMediaFlowTTL
	if resp.CacheTTLMillis != "" {
		if parsed, err := strconv.ParseInt(resp.CacheTTLMillis, 10, 64); err == nil && parsed > 0 {
			ttl = time.Duration(parsed) * time.Millisecond
		}
	}

	lc.cacheMu.Lock()
	lc.mediaFlowCache[chatMid] = cachedMediaFlow{
		flowMap:  resp.FlowMap,
		cachedAt: time.Now(),
		ttl:      ttl,
	}
	lc.cacheMu.Unlock()

	flow, exists := resp.FlowMap[strconv.Itoa(contentType)]
	if exists {
		return flow == 2
	}
	return true
}

func (lc *LineClient) isUserBlocked(mid string) bool {
	lc.cacheMu.Lock()
	defer lc.cacheMu.Unlock()
	return lc.blockedUsers[mid]
}

var _ bridgev2.NetworkAPI = (*LineClient)(nil)
var _ bridgev2.NetworkAPIWithUserID = (*LineClient)(nil)
var _ bridgev2.ReadReceiptHandlingNetworkAPI = (*LineClient)(nil)
var _ bridgev2.BackfillingNetworkAPI = (*LineClient)(nil)
var _ bridgev2.ReactionHandlingNetworkAPI = (*LineClient)(nil)

func (lc *LineClient) refreshAndSave(ctx context.Context) error {
	accessToken, refreshToken := lc.getTokens()
	if refreshToken == "" {
		return fmt.Errorf("no refresh token available")
	}

	client := newLineAPIClient(accessToken)
	res, err := client.RefreshAccessToken(refreshToken)
	if err != nil {
		return fmt.Errorf("failed to refresh token: %w", err)
	}

	accessToken, refreshToken = lc.setTokens(res.AccessToken, res.RefreshToken)

	// Rotating the main access token invalidates any OBS token derived from it,
	// so drop the cached one — the next OBS call will mint a fresh one.
	line.InvalidateOBSTokenCache()

	meta := lc.UserLogin.Metadata.(*UserLoginMetadata)
	meta.AccessToken = accessToken
	meta.RefreshToken = refreshToken
	meta.SessionInvalidated = false
	err = lc.UserLogin.Save(ctx)
	if err != nil {
		lc.UserLogin.Bridge.Log.Warn().Err(err).Msg("Failed to save refreshed tokens to DB")
	} else {
		lc.UserLogin.Bridge.Log.Info().Msg("Tokens refreshed and saved")
	}

	return nil
}

func (lc *LineClient) isRefreshRequired(err error) bool {
	return line.IsRefreshRequired(err)
}

func (lc *LineClient) isLoggedOut(err error) bool {
	return line.IsLoggedOut(err)
}

func (lc *LineClient) shouldAttemptTokenRecovery(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctx.Err() != nil || lc.superseded.Load() || lc.isSessionInvalidated() {
		return false
	}
	if lc.isLoggedOut(err) {
		lc.markLoggedOutByOtherClient(ctx, err)
		return false
	}
	return lc.isRefreshRequired(err) || line.IsUnauthorizedStatus(err)
}

func (lc *LineClient) markLoggedOutByOtherClient(ctx context.Context, err error) {
	// Serialize invalidation with token recovery. If a refresh/re-login was
	// already in flight, it may finish first, but this transition always runs
	// afterward so recovery cannot resurrect a forcefully logged-out session.
	lc.recoverMu.Lock()
	defer lc.recoverMu.Unlock()

	if lc.UserLogin == nil {
		lc.invalidateAccessToken()
		line.InvalidateOBSTokenCache()
		lc.cancelActiveRun()
		return
	}
	// superseded covers connector-ordered retirement. The identity check is a
	// final guard for bridgev2 client swaps that happen outside that lifecycle.
	if lc.superseded.Load() || (lc.UserLogin.Client != nil && lc.UserLogin.Client != lc) {
		if lc.UserLogin.Bridge != nil {
			lc.UserLogin.Bridge.Log.Debug().Err(err).Msg("Ignoring forced logout from stale LINE client")
		}
		lc.cancelActiveRun()
		return
	}
	sendState := lc.claimForcedLogoutState()
	lc.invalidateAccessToken()
	line.InvalidateOBSTokenCache()
	lc.saveSessionInvalidated(ctx)
	if sendState && lc.UserLogin.Bridge != nil {
		lc.UserLogin.Bridge.Log.Warn().Err(err).Msg("LINE session invalidated by another client; marking login bad credentials")
	}
	if sendState && lc.UserLogin.BridgeState != nil {
		lc.UserLogin.BridgeState.Send(status.BridgeState{
			StateEvent: status.StateBadCredentials,
			Error:      "line-logged-out",
			Message:    "LINE logged this Chrome Extension session out because another LINE client connected. Click Reconnect in Beeper to reconnect LINE.",
			UserAction: status.UserActionRelogin,
		})
	}
	lc.cancelActiveRun()
}

func (lc *LineClient) sendConnectedStateIfCurrent(ctx context.Context) bool {
	// Serialize the final startup state with forced logout and token recovery.
	// Whichever transition gets this lock last determines the visible state.
	lc.recoverMu.Lock()
	defer lc.recoverMu.Unlock()
	if ctx.Err() != nil || lc.superseded.Load() || lc.isSessionInvalidated() || !lc.hasAccessToken() {
		return false
	}
	lc.UserLogin.Bridge.Log.Info().Int("token_len", len(lc.getAccessToken())).Msg("LINE client connected; notifying bridge")
	lc.UserLogin.BridgeState.Send(status.BridgeState{
		StateEvent: status.StateConnected,
	})
	return true
}

func (lc *LineClient) saveSessionInvalidated(ctx context.Context) {
	if lc.UserLogin == nil || lc.UserLogin.UserLogin == nil {
		return
	}
	meta, ok := lc.UserLogin.Metadata.(*UserLoginMetadata)
	if !ok {
		return
	}
	meta.AccessToken = ""
	meta.SessionInvalidated = true
	if err := lc.UserLogin.Save(ctx); err != nil && lc.UserLogin.Bridge != nil {
		lc.UserLogin.Bridge.Log.Warn().Err(err).Msg("Failed to save LINE session invalidation")
	}
}

func (lc *LineClient) markMissingE2EEKey(ctx context.Context, err error) {
	if !errors.Is(err, e2ee.ErrMissingOwnPrivateKey) || lc.UserLogin == nil {
		return
	}
	lc.missingE2EEKeyMu.Lock()
	if lc.missingE2EEKeyMarked {
		lc.missingE2EEKeyMu.Unlock()
		return
	}
	lc.missingE2EEKeyMarked = true
	if meta, ok := lc.UserLogin.Metadata.(*UserLoginMetadata); ok {
		needsSave := !meta.ForceFullE2EELogin || meta.Certificate != ""
		meta.ForceFullE2EELogin = true
		meta.Certificate = ""
		if !needsSave {
			lc.missingE2EEKeyMu.Unlock()
			return
		}
		lc.missingE2EEKeyMu.Unlock()
		if errSave := lc.UserLogin.Save(ctx); errSave != nil && lc.UserLogin.Bridge != nil {
			lc.UserLogin.Bridge.Log.Warn().Err(errSave).Msg("Failed to save LINE E2EE reconnect requirement")
		}
	} else {
		lc.missingE2EEKeyMu.Unlock()
	}
	if lc.UserLogin.Bridge != nil {
		lc.UserLogin.Bridge.Log.Warn().Err(err).Msg("LINE E2EE private key missing; marking login for full reconnect")
	}
	lc.UserLogin.BridgeState.Send(status.BridgeState{
		StateEvent: status.StateBadCredentials,
		Error:      "line-e2ee-key-missing",
		Message:    lineMissingE2EEKeyMessage,
		UserAction: status.UserActionRelogin,
	})
}

func (lc *LineClient) applyRefreshedLoginE2EEKeys(meta *UserLoginMetadata, res *line.LoginResult, exported map[string]string) {
	lc.missingE2EEKeyMu.Lock()
	defer lc.missingE2EEKeyMu.Unlock()
	applyExportedLoginE2EEKeys(meta, res, exported)
	lc.missingE2EEKeyMarked = false
}

// recoverToken attempts to restore a valid session by refreshing, then re-logging in.
// Returns nil on success. On failure the caller should send StateBadCredentials.
func (lc *LineClient) recoverToken(ctx context.Context) error {
	return lc.recoverTokenWith(ctx, lc.refreshAndSave, lc.tryLogin)
}

func (lc *LineClient) recoverTokenWith(
	ctx context.Context,
	refresh func(context.Context) error,
	relogin func(context.Context) error,
) error {
	return lc.runTokenRecovery(ctx, func(ctx context.Context) error {
		if err := refresh(ctx); err == nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			lc.UserLogin.Bridge.Log.Info().Msg("Token recovered via refresh")
			return nil
		} else if ctx.Err() != nil {
			return ctx.Err()
		} else if lc.isLoggedOut(err) || errors.Is(err, errLineSessionInvalidated) {
			return err
		}
		lc.UserLogin.Bridge.Log.Info().Msg("Refresh failed, attempting re-login with stored credentials...")
		err := relogin(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	})
}

func (lc *LineClient) runTokenRecovery(ctx context.Context, recover func(context.Context) error) error {
	lc.recoverMu.Lock()
	defer lc.recoverMu.Unlock()

	if ctx.Err() != nil {
		return ctx.Err()
	}
	if lc.recoveryStopped || lc.superseded.Load() {
		return errLineClientSuperseded
	}
	if lc.isSessionInvalidated() {
		return errLineSessionInvalidated
	}

	if !lc.recoverTime.IsZero() && time.Since(lc.recoverTime) < recentTokenRecoveryWindow {
		return nil
	}

	if err := recover(ctx); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if lc.superseded.Load() {
		return errLineClientSuperseded
	}
	if lc.isSessionInvalidated() {
		return errLineSessionInvalidated
	}
	lc.recoverTime = time.Now()
	return nil
}

func (lc *LineClient) Connect(ctx context.Context) {
	ctx, run, started := lc.beginRun(ctx)
	if !started {
		return
	}
	defer lc.wg.Done()
	workersStarted := false
	defer func() {
		if !workersStarted {
			run.cancel()
		}
	}()

	lc.cacheMu.Lock()
	if lc.peerKeys == nil {
		lc.peerKeys = make(map[string]peerKeyInfo)
	}
	if lc.blockedUsers == nil {
		lc.blockedUsers = make(map[string]bool)
	}
	if lc.contactCache == nil {
		lc.contactCache = make(map[string]cachedContact)
	}
	if lc.groupMemberCache == nil {
		lc.groupMemberCache = make(map[string][]string)
	}
	if lc.knownMemberChatMIDs == nil {
		lc.knownMemberChatMIDs = make(map[string]struct{})
	}
	lc.cacheMu.Unlock()
	lc.reqSeqMu.Lock()
	if lc.sentReqSeqs == nil {
		lc.sentReqSeqs = make(map[int]time.Time)
	}
	lc.reqSeqMu.Unlock()

	if lc.Mid == "" {
		if meta, ok := lc.UserLogin.Metadata.(*UserLoginMetadata); ok {
			lc.Mid = meta.Mid
		}
	}
	if !lc.hasAccessToken() {
		if lc.isSessionInvalidated() {
			lc.markLoggedOutByOtherClient(ctx, errLineSessionInvalidated)
			return
		}
		if err := lc.runTokenRecovery(ctx, lc.tryLogin); err != nil {
			if ctx.Err() != nil || lc.superseded.Load() || errors.Is(err, errLineSessionInvalidated) {
				return
			}
			if lc.isLoggedOut(err) {
				lc.markLoggedOutByOtherClient(ctx, err)
				return
			}
			lc.UserLogin.BridgeState.Send(status.BridgeState{
				StateEvent: status.StateBadCredentials,
				Error:      "line-login-failed",
				Message:    err.Error(),
				UserAction: status.UserActionRelogin,
			})
			return
		}
	}

	// Verify the token is still valid before proceeding
	if err := lc.ensureValidToken(ctx); err != nil {
		if ctx.Err() != nil {
			return
		}
		if lc.isLoggedOut(err) {
			lc.markLoggedOutByOtherClient(ctx, err)
			return
		}
		lc.UserLogin.BridgeState.Send(status.BridgeState{
			StateEvent: status.StateBadCredentials,
			Error:      "line-token-expired",
			Message:    fmt.Sprintf("session expired and could not be restored: %v", err),
			UserAction: status.UserActionRelogin,
		})
		return
	}
	// LINE Chrome disables mobile notifications while its session is active by
	// default. Explicitly clear that setting once per bridge connection so the
	// user's phone continues receiving notifications.
	lc.configurePhoneNotifications(ctx)
	if ctx.Err() != nil {
		return
	}
	if !lc.sendConnectedStateIfCurrent(ctx) {
		return
	}

	// Initialize E2EE manager and load keys
	mgr, err := e2ee.NewManager()
	if err != nil {
		lc.UserLogin.Bridge.Log.Warn().Err(err).Msg("Failed to init E2EE manager")
	} else {
		lc.E2EE = mgr
		if meta, ok := lc.UserLogin.Metadata.(*UserLoginMetadata); ok && len(meta.ExportedKeyMap) > 0 {
			if err := mgr.LoadMyKeyFromExportedMap(meta.ExportedKeyMap); err != nil {
				lc.UserLogin.Bridge.Log.Warn().Err(err).Msg("Failed to load E2EE key from DB metadata")
			} else {
				lc.UserLogin.Bridge.Log.Info().Int("exported_keys", len(meta.ExportedKeyMap)).Msg("Loaded E2EE key from DB metadata")
			}
		}

		// Storage key is optional for runtime decrypt/encrypt; try it for file support
		client := lc.newClient()
		ei3, err := client.GetEncryptedIdentityV3()
		if err != nil {
			lc.UserLogin.Bridge.Log.Warn().Err(err).Msg("Failed to fetch EncryptedIdentityV3")
		} else {
			if err := mgr.InitStorage(ei3.WrappedNonce, ei3.KDFParameter1, ei3.KDFParameter2); err != nil {
				lc.UserLogin.Bridge.Log.Warn().Err(err).Msg("Failed to init storage key")
			} else if data, err := mgr.LoadSecureDataFromFile(string(lc.UserLogin.ID)); err == nil {
				if err := mgr.LoadMyKeyFromSecureData(data); err != nil {
					lc.UserLogin.Bridge.Log.Warn().Err(err).Msg("Failed to load E2EE key from secure data")
				}
			}
		}
	}

	// Seed the last-known block list before fetching a fresh copy, so a
	// transient LINE API failure doesn't reopen intentionally blocked DMs.
	lc.cacheMu.Lock()
	for mid := range lc.metadataBlockedContacts() {
		lc.blockedUsers[mid] = true
	}
	lc.cacheMu.Unlock()

	// Fetch initial blocked contacts list before starting sync loops.
	if newlyUnblocked, err := lc.refreshBlockedContacts(ctx); err != nil {
		lc.UserLogin.Bridge.Log.Warn().Err(err).Msg("Failed to fetch blocked contacts, continuing with last known block list")
	} else {
		for _, mid := range newlyUnblocked {
			lc.queueUnblockedDMRestore(ctx, mid, "startup")
		}
	}

	// Create/sync group portals before message prefetching or SSE polling can
	// deliver messages. Otherwise an existing LINE group whose Matrix room
	// doesn't exist yet may be created by the first message, which makes the
	// sender's existing membership look like a fresh join.
	lc.wg.Add(1)
	lc.syncChats(ctx)
	if ctx.Err() != nil {
		return
	}

	lc.wg.Add(3)
	go lc.syncDMChats(ctx)
	go lc.prefetchMessages(ctx)
	go lc.pollLoop(ctx)
	workersStarted = true
}

func (lc *LineClient) tryLogin(ctx context.Context) error {
	var email, password, certificate string
	if meta, ok := lc.UserLogin.Metadata.(*UserLoginMetadata); ok {
		email = meta.Email
		password = meta.Password
		certificate = meta.Certificate
	}

	if email == "" || password == "" {
		return fmt.Errorf("no stored credentials available for re-login")
	}

	lc.UserLogin.Bridge.Log.Info().
		Str("email", email).
		Bool("has_certificate", certificate != "").
		Msg("Attempting to login with email/password...")
	res, err := loginWithCredentials(email, password, certificate)
	if err != nil {
		return fmt.Errorf("login failed: %w", err)
	}
	if res.AuthToken == "" {
		// No usable verifier means LINE didn't engage the PIN-based flow.
		// Bail out before emitting a misleading "PIN required" bridge state —
		// the locally-generated PIN here was never sent to LINE and is useless.
		if res.Verifier == "" {
			return fmt.Errorf("login requires interaction but no verifier returned")
		}

		pin := res.Pin
		if res.PinCode != "" {
			pin = res.PinCode
		}
		if pin != "" {
			lc.UserLogin.Bridge.Log.Warn().Msg("PIN verification required — check your LINE mobile app to complete re-login")
			// Send the PIN via bridge state so the user sees it in their Matrix client
			lc.UserLogin.BridgeState.Send(status.BridgeState{
				StateEvent: status.StateConnecting,
				Error:      "line-pin-required",
				Message:    fmt.Sprintf("Enter this PIN on your LINE mobile app: %s", pin),
				UserAction: status.UserActionRelogin,
			})
		}

		lc.UserLogin.Bridge.Log.Info().Msg("Waiting for PIN verification on mobile device...")
		waitClient := newLineAPIClient("")
		waitRes, err := waitClient.WaitForLogin(res.Verifier, res.NoE2EE)
		if err != nil {
			return fmt.Errorf("PIN verification failed: %w", err)
		}
		if waitRes.AuthToken == "" {
			return fmt.Errorf("PIN verification completed but no auth token received")
		}
		// Replace res with the verified result
		res = waitRes
	}
	accessToken := res.AuthToken
	refreshToken := ""
	if res.TokenV3IssueResult != nil {
		if res.TokenV3IssueResult.AccessToken != "" {
			accessToken = res.TokenV3IssueResult.AccessToken
		}
		if res.TokenV3IssueResult.RefreshToken != "" {
			refreshToken = res.TokenV3IssueResult.RefreshToken
		}
	}
	accessToken, refreshToken = lc.setTokens(accessToken, refreshToken)

	// Re-login replaces the main access token, which invalidates any cached
	// OBS token derived from the previous one.
	line.InvalidateOBSTokenCache()

	if res.Mid != "" {
		lc.Mid = res.Mid
		if meta, ok := lc.UserLogin.Metadata.(*UserLoginMetadata); ok {
			meta.Mid = res.Mid
		}
	}

	// Save the new tokens and updated certificate to metadata
	if meta, ok := lc.UserLogin.Metadata.(*UserLoginMetadata); ok {
		meta.AccessToken = accessToken
		meta.RefreshToken = refreshToken
		meta.SessionInvalidated = false
		if res.Certificate != "" {
			meta.Certificate = res.Certificate
		}
		if err := lc.refreshLoginE2EEKeys(res, meta, newLineAPIClient(accessToken)); err != nil {
			lc.UserLogin.Bridge.Log.Warn().Err(err).Msg("Failed to refresh E2EE keys after re-login")
		}
		if err := lc.UserLogin.Save(ctx); err != nil {
			lc.UserLogin.Bridge.Log.Warn().Err(err).Msg("Failed to save new tokens to DB")
		}
	}

	lc.UserLogin.Bridge.Log.Info().Msg("Login successful!")
	return nil
}

func (lc *LineClient) refreshLoginE2EEKeys(res *line.LoginResult, meta *UserLoginMetadata, client *line.Client) error {
	if res.EncryptedKeyChain == "" || res.E2EEPublicKey == "" {
		return nil
	}
	mgr, exported, err := exportLoginE2EEKeys(res, client)
	if err != nil {
		return err
	}
	if lc.E2EE != nil {
		if err := lc.E2EE.LoadMyKeyFromExportedMap(exported); err != nil {
			return fmt.Errorf("load exported keys into active E2EE manager: %w", err)
		}
	}
	lc.applyRefreshedLoginE2EEKeys(meta, res, exported)
	if err := mgr.SaveSecureDataToFile(loginSecureDataID(meta, string(lc.UserLogin.ID)), map[string]any{"exportedKeyMap": exported}); err != nil {
		lc.UserLogin.Bridge.Log.Warn().Err(err).Msg("Failed to save E2EE secure data after re-login")
	}
	lc.UserLogin.Bridge.Log.Info().Int("keys", len(exported)).Msg("Refreshed E2EE keys after re-login")
	return nil
}

func (lc *LineClient) ensureValidToken(ctx context.Context) error {
	return lc.ensureValidTokenWith(
		ctx,
		func(ctx context.Context) error {
			_, err := getProfileWithToken(ctx, lc.getAccessToken())
			return err
		},
		lc.refreshAndSave,
		lc.tryLogin,
	)
}

func (lc *LineClient) ensureValidTokenWith(
	ctx context.Context,
	profile func(context.Context) error,
	refresh func(context.Context) error,
	relogin func(context.Context) error,
) error {
	err := profile(ctx)
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}

	if lc.isLoggedOut(err) {
		return err
	}

	if !lc.isRefreshRequired(err) {
		lc.UserLogin.Bridge.Log.Warn().Err(err).Msg("GetProfile failed with non-auth error, continuing anyway")
		return nil
	}

	lc.UserLogin.Bridge.Log.Info().Msg("Access token expired, attempting refresh...")
	return lc.recoverTokenWith(ctx, refresh, relogin)
}

func (lc *LineClient) Disconnect() {
	// Disconnect is terminal for this NetworkAPI instance. Framework reconnects
	// create a replacement client, so late handlers on this one must not mutate
	// the shared UserLogin or start token recovery.
	lc.superseded.Store(true)
	lc.runMu.Lock()
	lc.stopped = true
	run := lc.activeRun
	if run != nil {
		run.cancel()
	}
	lc.runMu.Unlock()
	lc.wg.Wait()
	// markLoggedOutByOtherClient and token recovery both hold recoverMu while
	// applying/persisting session state. Drain any call that began before the
	// superseded flag was visible before a replacement installs new metadata.
	lc.recoverMu.Lock()
	lc.recoveryStopped = true
	lc.recoverMu.Unlock()
}

func (lc *LineClient) IsLoggedIn() bool { return lc.hasAccessToken() }

func (lc *LineClient) GetUserID() networkid.UserID {
	return makeUserID(lc.Mid)
}

func (lc *LineClient) LogoutRemote(ctx context.Context) {}

func (lc *LineClient) midOrFallback() string {
	if lc.Mid != "" {
		return lc.Mid
	}
	return string(lc.UserLogin.ID)
}

func makeUserID(userID string) networkid.UserID { return networkid.UserID(userID) }

func makePortalID(userID string) networkid.PortalID { return networkid.PortalID(userID) }

func guessToType(mid string) ToType {
	if strings.HasPrefix(strings.ToLower(mid), "c") {
		return ToGroup
	}
	if strings.HasPrefix(strings.ToLower(mid), "r") {
		return ToRoom
	}
	return ToUser
}
