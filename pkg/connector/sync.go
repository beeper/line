package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/ptr"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/bridgev2/status"
	"maunium.net/go/mautrix/event"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

const (
	prefetchMessagesConcurrency               = 4
	messageBoxPageLimit                       = 100
	startupBackfillMessageLimit               = 50
	unblockBackfillPortalWaitTimeout          = time.Minute
	unblockBackfillPortalPollInterval         = 500 * time.Millisecond
	unblockBackfillFrameworkGrace             = 10 * time.Second
	unblockBackfillFrameworkCompletionTimeout = 2 * time.Minute
	groupPortalCreateWait                     = 30 * time.Second
	beeperExcludeFromTimelineKey              = "com.beeper.exclude_from_timeline"
	defaultReceiveAuthProbeInterval           = 150 * time.Second
)

var (
	errReceiveAuthProbeDue               = errors.New("receive auth probe due")
	errUnblockBackfillHandledByFramework = errors.New("unblock backfill handled by framework")
)

type unblockBackfillOwner uint8

const (
	unblockBackfillOwnerNone unblockBackfillOwner = iota
	unblockBackfillOwnerFramework
	unblockBackfillOwnerManual
)

// unblockBackfillState gives either bridgev2's normal forward backfill or the
// manual silent fallback exclusive ownership of one unblocked chat.
type unblockBackfillState struct {
	mu               sync.Mutex
	owner            unblockBackfillOwner
	frameworkStarted chan struct{}
	done             chan struct{}
	doneOnce         sync.Once
}

func newUnblockBackfillState() *unblockBackfillState {
	return &unblockBackfillState{
		frameworkStarted: make(chan struct{}),
		done:             make(chan struct{}),
	}
}

func (state *unblockBackfillState) claimFramework() bool {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.owner != unblockBackfillOwnerNone {
		return false
	}
	state.owner = unblockBackfillOwnerFramework
	close(state.frameworkStarted)
	return true
}

func (state *unblockBackfillState) claimManual() bool {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.owner != unblockBackfillOwnerNone {
		return false
	}
	state.owner = unblockBackfillOwnerManual
	return true
}

func (state *unblockBackfillState) complete() {
	state.doneOnce.Do(func() {
		close(state.done)
	})
}

type manualUnblockBackfillContextKey struct{}

var (
	getLastOpRevisionWithClient = func(ctx context.Context, client *line.Client) (int64, error) {
		return client.GetLastOpRevisionContext(ctx)
	}
	listenSSEWithClient = func(client *line.Client, ctx context.Context, localRev int64, handler func(eventType, data string)) error {
		return client.ListenSSE(ctx, localRev, handler)
	}
	sseReconnectDelay          = 3 * time.Second
	receiveAuthProbeInterval   = defaultReceiveAuthProbeInterval
	receiveAuthProbeNow        = time.Now
	newReceiveAuthProbeContext = func(parent context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
		return context.WithDeadlineCause(parent, deadline, errReceiveAuthProbeDue)
	}
	runSilentUnblockBackfill = func(ctx context.Context, lc *LineClient, mid string) {
		lc.silentBackfillRecentMessages(ctx, mid, startupBackfillMessageLimit)
	}
)

func (lc *LineClient) getMessageBoxesWithRecovery(ctx context.Context, opts line.MessageBoxesOptions) (*line.MessageBoxesResponse, error) {
	client := lc.newClient()
	res, err := client.GetMessageBoxes(opts)
	if err != nil && lc.shouldAttemptTokenRecovery(ctx, err) {
		if errRecover := lc.recoverToken(ctx); errRecover == nil {
			client = lc.newClient()
			res, err = client.GetMessageBoxes(opts)
		}
	}
	return res, err
}

func (lc *LineClient) fetchAllMessageBoxes(ctx context.Context, opts line.MessageBoxesOptions) ([]line.MessageBox, error) {
	var boxes []line.MessageBox
	seenCursors := make(map[string]struct{})
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		res, err := lc.getMessageBoxesWithRecovery(ctx, opts)
		if err != nil {
			return nil, err
		}
		boxes = append(boxes, res.MessageBoxes...)
		if !res.HasNext || len(res.MessageBoxes) == 0 {
			return boxes, nil
		}

		nextCursor := res.MessageBoxes[len(res.MessageBoxes)-1].ID
		if nextCursor == "" {
			return boxes, nil
		}
		if _, ok := seenCursors[nextCursor]; ok {
			return boxes, nil
		}
		seenCursors[nextCursor] = struct{}{}
		opts.MinChatID = nextCursor
	}
}

func (lc *LineClient) refreshBlockedContacts(ctx context.Context) ([]string, error) {
	client := lc.newClient()
	blockedMIDs, err := client.GetBlockedContactIds()
	if err != nil && lc.shouldAttemptTokenRecovery(ctx, err) {
		if errRecover := lc.recoverToken(ctx); errRecover == nil {
			client = lc.newClient()
			blockedMIDs, err = client.GetBlockedContactIds()
		}
	}
	if err != nil {
		return nil, err
	}

	blockedUsers := make(map[string]bool, len(blockedMIDs))
	for _, mid := range blockedMIDs {
		blockedUsers[mid] = true
	}

	metadataBlockedUsers := lc.metadataBlockedContacts()
	lc.cacheMu.Lock()
	previousBlockedUsers := make(map[string]bool, len(lc.blockedUsers)+len(metadataBlockedUsers))
	for mid := range lc.blockedUsers {
		previousBlockedUsers[mid] = true
	}
	for mid := range metadataBlockedUsers {
		previousBlockedUsers[mid] = true
	}
	var newlyUnblocked []string
	for mid := range previousBlockedUsers {
		if !blockedUsers[mid] {
			newlyUnblocked = append(newlyUnblocked, mid)
		}
	}
	lc.blockedUsers = blockedUsers
	lc.cacheMu.Unlock()

	lc.saveBlockedContacts(ctx, blockedUsers)
	lc.UserLogin.Bridge.Log.Info().Int("count", len(blockedMIDs)).Msg("Refreshed blocked contacts")
	return newlyUnblocked, nil
}

func (lc *LineClient) metadataBlockedContacts() map[string]bool {
	blockedUsers := make(map[string]bool)
	meta, ok := lc.UserLogin.Metadata.(*UserLoginMetadata)
	if !ok {
		return blockedUsers
	}
	for _, mid := range meta.BlockedMIDs {
		blockedUsers[mid] = true
	}
	return blockedUsers
}

func (lc *LineClient) saveBlockedContacts(ctx context.Context, blockedUsers map[string]bool) {
	meta, ok := lc.UserLogin.Metadata.(*UserLoginMetadata)
	if !ok {
		return
	}

	mids := make([]string, 0, len(blockedUsers))
	for mid := range blockedUsers {
		mids = append(mids, mid)
	}
	sort.Strings(mids)
	meta.BlockedMIDs = mids
	if err := lc.UserLogin.Save(ctx); err != nil {
		lc.UserLogin.Bridge.Log.Warn().Err(err).Msg("Failed to save blocked contacts snapshot")
	}
}

func (lc *LineClient) saveBlockedContactsSnapshot(ctx context.Context) {
	lc.cacheMu.Lock()
	blockedUsers := make(map[string]bool, len(lc.blockedUsers))
	for mid := range lc.blockedUsers {
		blockedUsers[mid] = true
	}
	lc.cacheMu.Unlock()

	lc.saveBlockedContacts(ctx, blockedUsers)
}

func (lc *LineClient) syncDMChats(ctx context.Context) {
	defer lc.wg.Done()

	opts := line.MessageBoxesOptions{
		ActiveOnly:                     true,
		MessageBoxCountLimit:           messageBoxPageLimit,
		WithUnreadCount:                false,
		LastMessagesPerMessageBoxCount: 0,
	}

	messageBoxes, err := lc.fetchAllMessageBoxes(ctx, opts)
	if err != nil {
		lc.UserLogin.Bridge.Log.Warn().Err(err).Msg("Failed to fetch message boxes for DM sync")
		return
	}

	for _, box := range messageBoxes {
		mid := box.ID
		lowerMid := strings.ToLower(mid)
		// Skip group chats — they're handled by syncChats
		if strings.HasPrefix(lowerMid, "c") || strings.HasPrefix(lowerMid, "r") {
			continue
		}
		// Skip DMs with blocked contacts so a fullSync doesn't recreate a portal
		// we just deleted in response to OpBlockContact.
		if lc.isUserBlocked(mid) {
			continue
		}

		lc.queueDMChatResync(ctx, mid, false, false)
	}
}

// queueDMChatResync emits a ChatResync event with full DM ChatInfo.
// If createPortal is true, the framework will create the portal when it
// doesn't already exist (e.g. after the DM was deleted on block).
// If forceBackfill is true, an existing portal will run a forward backfill, and
// a newly-created portal will backfill immediately after Matrix room creation.
func (lc *LineClient) queueDMChatResync(ctx context.Context, mid string, createPortal, forceBackfill bool) {
	contact := lc.getContact(ctx, mid)
	dmType := database.RoomTypeDM
	chatName := contact.EffectiveDisplayName()
	portalKey := networkid.PortalKey{ID: makePortalID(mid), Receiver: lc.UserLogin.ID}
	var checkNeedsBackfill func(ctx context.Context, latestMessage *database.Message) (bool, error)
	if forceBackfill {
		checkNeedsBackfill = func(ctx context.Context, latestMessage *database.Message) (bool, error) {
			return true, nil
		}
	}
	lc.UserLogin.Bridge.QueueRemoteEvent(lc.UserLogin, &simplevent.ChatResync{
		EventMeta: simplevent.EventMeta{
			Type:         bridgev2.RemoteEventChatResync,
			PortalKey:    portalKey,
			Timestamp:    time.Now(),
			CreatePortal: createPortal,
		},
		ChatInfo: &bridgev2.ChatInfo{
			Type:        &dmType,
			Name:        &chatName,
			Avatar:      lc.avatarFromPicturePath(contact.PicturePath),
			CanBackfill: forceBackfill,
			Members: &bridgev2.ChatMemberList{
				IsFull:                     true,
				ExcludeChangesFromTimeline: true,
				Members: []bridgev2.ChatMember{
					{
						EventSender: bridgev2.EventSender{
							IsFromMe: true,
							Sender:   networkid.UserID(lc.UserLogin.ID),
						},
						Membership: event.MembershipJoin,
						PowerLevel: ptr.Ptr(100),
					},
					{
						EventSender: bridgev2.EventSender{
							Sender: makeUserID(mid),
						},
						Membership: event.MembershipJoin,
						PowerLevel: ptr.Ptr(0),
					},
				},
			},
			ExcludeChangesFromTimeline: true,
		},
		CheckNeedsBackfillFunc: checkNeedsBackfill,
	})
}

func (lc *LineClient) beginUnblockBackfill(mid string) (*unblockBackfillState, bool) {
	state := newUnblockBackfillState()
	actual, loaded := lc.unblockBackfills.LoadOrStore(mid, state)
	if loaded {
		return actual.(*unblockBackfillState), false
	}
	return state, true
}

func (lc *LineClient) getUnblockBackfill(mid string) *unblockBackfillState {
	state, ok := lc.unblockBackfills.Load(mid)
	if !ok {
		return nil
	}
	return state.(*unblockBackfillState)
}

func (lc *LineClient) finishUnblockBackfill(mid string, state *unblockBackfillState) {
	lc.unblockBackfills.CompareAndDelete(mid, state)
}

func waitForUnblockBackfillPortal(
	ctx context.Context,
	timeout time.Duration,
	pollInterval time.Duration,
	frameworkStarted <-chan struct{},
	lookup func(context.Context) (*bridgev2.Portal, error),
) (*bridgev2.Portal, error) {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	var lastLookupErr error
	for {
		select {
		case <-frameworkStarted:
			return nil, errUnblockBackfillHandledByFramework
		default:
		}

		portal, err := lookup(waitCtx)
		if err != nil {
			lastLookupErr = err
		} else if portal != nil && portal.MXID != "" {
			return portal, nil
		}

		select {
		case <-frameworkStarted:
			return nil, errUnblockBackfillHandledByFramework
		case <-waitCtx.Done():
			if lastLookupErr != nil {
				return nil, fmt.Errorf("portal did not become ready after lookup error: %w", lastLookupErr)
			}
			return nil, waitCtx.Err()
		case <-ticker.C:
		}
	}
}

func (lc *LineClient) waitForFrameworkUnblockBackfill(ctx context.Context, log zerolog.Logger, state *unblockBackfillState) {
	timer := time.NewTimer(unblockBackfillFrameworkCompletionTimeout)
	defer timer.Stop()
	select {
	case <-state.done:
		log.Debug().Msg("Framework unblock backfill finished")
	case <-ctx.Done():
	case <-timer.C:
		log.Warn().Msg("Timed out waiting for framework unblock backfill to finish")
	}
}

func (lc *LineClient) silentBackfillRecentMessages(ctx context.Context, chatMID string, limit int) {
	log := lc.UserLogin.Bridge.Log.With().
		Str("chat_mid", chatMID).
		Str("action", "silent unblock backfill").
		Logger()
	ctx = log.WithContext(ctx)

	state := lc.getUnblockBackfill(chatMID)
	var frameworkStarted <-chan struct{}
	if state != nil {
		frameworkStarted = state.frameworkStarted
	}
	portalKey := networkid.PortalKey{ID: makePortalID(chatMID), Receiver: lc.UserLogin.ID}
	portal, err := waitForUnblockBackfillPortal(
		ctx,
		unblockBackfillPortalWaitTimeout,
		unblockBackfillPortalPollInterval,
		frameworkStarted,
		func(ctx context.Context) (*bridgev2.Portal, error) {
			return lc.UserLogin.Bridge.GetExistingPortalByKey(ctx, portalKey)
		},
	)
	if errors.Is(err, errUnblockBackfillHandledByFramework) {
		log.Debug().Msg("Skipping manual unblock backfill because framework backfill started")
		lc.waitForFrameworkUnblockBackfill(ctx, log, state)
		return
	} else if err != nil {
		if ctx.Err() != nil {
			log.Debug().Err(ctx.Err()).Msg("Stopped waiting for restored portal")
		} else {
			log.Warn().Err(err).Dur("wait_timeout", unblockBackfillPortalWaitTimeout).Msg("Restored portal did not become ready for silent unblock backfill")
		}
		return
	}

	if state != nil {
		timer := time.NewTimer(unblockBackfillFrameworkGrace)
		select {
		case <-state.frameworkStarted:
			timer.Stop()
			log.Debug().Msg("Skipping manual unblock backfill because framework backfill started")
			lc.waitForFrameworkUnblockBackfill(ctx, log, state)
			return
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if !state.claimManual() {
			log.Debug().Msg("Skipping manual unblock backfill because another backfill claimed the chat")
			lc.waitForFrameworkUnblockBackfill(ctx, log, state)
			return
		}
	}

	if lc.isUserBlocked(chatMID) {
		log.Debug().Msg("Skipping silent unblock backfill because contact is blocked again")
		return
	}

	capabilities := lc.UserLogin.Bridge.Matrix.GetCapabilities()
	if capabilities == nil || !capabilities.BatchSending {
		log.Debug().Msg("Skipping unblock fallback because silent batch sending is unavailable")
		return
	}
	log.Info().Msg("Running silent unblock fallback backfill")
	ctx = context.WithValue(ctx, manualUnblockBackfillContextKey{}, true)

	resp, err := lc.FetchMessages(ctx, bridgev2.FetchMessagesParams{
		Portal:  portal,
		Forward: true,
		Count:   limit,
	})
	if err != nil {
		log.Warn().Err(err).Msg("Failed to fetch messages for silent unblock backfill")
		return
	} else if resp == nil {
		log.Warn().Msg("No response returned for silent unblock backfill")
		return
	} else if len(resp.Messages) == 0 {
		if resp.CompleteCallback != nil {
			resp.CompleteCallback()
		}
		log.Debug().Msg("No messages found for silent unblock backfill")
		return
	}

	latestMessage, err := lc.UserLogin.Bridge.DB.Message.GetLastPartAtOrBeforeTime(
		ctx,
		portal.PortalKey,
		time.Now().Add(10*time.Second),
	)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to get latest message before silent unblock backfill")
		return
	}
	// bridgev2 does not expose a public API for forcing a silent forward backfill
	// when the normal resync path is disabled by the runtime backfill config.
	//lint:ignore SA1019 The fallback must use bridgev2's batch backfill internals to remain notification-free.
	portalInternals := portal.Internal()
	resp.Messages = portalInternals.CutoffMessages(ctx, resp.Messages, true, true, latestMessage)
	if len(resp.Messages) == 0 {
		if resp.CompleteCallback != nil {
			resp.CompleteCallback()
		}
		log.Debug().Msg("No new messages left for silent unblock backfill")
		return
	}

	messageCount := len(resp.Messages)
	complete := resp.CompleteCallback
	log.Info().Int("message_count", messageCount).Msg("Sending silent unblock backfill")
	portalInternals.SendBackfill(ctx, lc.UserLogin, resp.Messages, true, true, false, func() {
		if complete != nil {
			complete()
		}
		log.Info().Int("message_count", messageCount).Msg("Finished silent unblock backfill")
	})
}

func (lc *LineClient) runUnblockBackfillFallback(ctx context.Context, mid string) {
	if lc.isUserBlocked(mid) {
		lc.UserLogin.Bridge.Log.Debug().
			Str("mid", mid).
			Msg("Skipping unblock fallback backfill because contact is blocked again")
		return
	}

	runSilentUnblockBackfill(ctx, lc, mid)
}

func (lc *LineClient) queueUnblockBackfillFallback(ctx context.Context, mid string) {
	state := lc.getUnblockBackfill(mid)
	lc.wg.Add(1)
	go func() {
		defer lc.wg.Done()
		if state != nil {
			defer lc.finishUnblockBackfill(mid, state)
		}
		lc.runUnblockBackfillFallback(ctx, mid)
	}()
}

func (lc *LineClient) queueUnblockedDMRestore(ctx context.Context, mid, reason string) {
	if isChatMID(mid) {
		return
	}
	capabilities := lc.UserLogin.Bridge.Matrix.GetCapabilities()
	if capabilities == nil || !capabilities.BatchSending {
		lc.UserLogin.Bridge.Log.Debug().
			Str("mid", mid).
			Msg("Restoring unblocked DM without history because silent batch sending is unavailable")
		lc.queueDMChatResync(ctx, mid, true, false)
		return
	}
	_, queued := lc.beginUnblockBackfill(mid)
	if !queued {
		lc.UserLogin.Bridge.Log.Debug().
			Str("mid", mid).
			Msg("Unblock backfill is already queued")
		return
	}
	lc.UserLogin.Bridge.Log.Info().
		Str("mid", mid).
		Str("reason", reason).
		Msg("Restoring unblocked DM with backfill")
	lc.queueDMChatResync(ctx, mid, true, true)
	lc.queueUnblockBackfillFallback(ctx, mid)
}

// FetchMessages implements bridgev2.BackfillingNetworkAPI. It powers silent,
// batch-sent history backfill. It is currently triggered when a DM portal is
// recreated after the contact is unblocked (see queueDMChatResync), repopulating
// the restored chat's recent history without notifying for every old message.
// Only the newest params.Count messages are returned; there is no older-history
// pagination, so backward fetches return an empty, final batch.
func (lc *LineClient) FetchMessages(ctx context.Context, params bridgev2.FetchMessagesParams) (*bridgev2.FetchMessagesResponse, error) {
	// We only populate the most recent messages; we don't paginate further back.
	if !params.Forward {
		return &bridgev2.FetchMessagesResponse{HasMore: false}, nil
	}

	chatMID := string(params.Portal.PortalKey.ID)
	unblockState := lc.getUnblockBackfill(chatMID)
	manualUnblockBackfill, _ := ctx.Value(manualUnblockBackfillContextKey{}).(bool)
	if unblockState != nil && !manualUnblockBackfill {
		capabilities := lc.UserLogin.Bridge.Matrix.GetCapabilities()
		if capabilities == nil || !capabilities.BatchSending {
			return &bridgev2.FetchMessagesResponse{HasMore: false, MarkRead: true}, nil
		}
		if !unblockState.claimFramework() {
			lc.UserLogin.Bridge.Log.Debug().
				Str("chat_mid", chatMID).
				Msg("Skipping duplicate unblock backfill fetch")
			return &bridgev2.FetchMessagesResponse{HasMore: false, MarkRead: true}, nil
		}
	}
	limit := params.Count
	if limit <= 0 {
		limit = 50
	}

	client := lc.newClient()
	msgs, err := client.GetRecentMessagesV2(chatMID, limit)
	if err != nil && lc.shouldAttemptTokenRecovery(ctx, err) {
		if errRecover := lc.recoverToken(ctx); errRecover == nil {
			client = lc.newClient()
			msgs, err = client.GetRecentMessagesV2(chatMID, limit)
		}
	}
	if err != nil {
		if unblockState != nil {
			unblockState.complete()
		}
		return nil, fmt.Errorf("failed to fetch recent messages for backfill: %w", err)
	}

	// GetRecentMessagesV2 returns newest-first; backfill wants oldest-first.
	backfillMsgs := make([]*bridgev2.BackfillMessage, 0, len(msgs))
	for i := len(msgs) - 1; i >= 0; i-- {
		msg := msgs[i]
		lc.cacheGroupMembersFromMessage(chatMID, msg)
		if !isBridgeableContentType(msg) {
			continue
		}

		sender := bridgev2.EventSender{
			Sender:   makeUserID(msg.From),
			IsFromMe: msg.From == lc.Mid,
		}
		intent, ok := params.Portal.GetIntentFor(ctx, sender, lc.UserLogin, bridgev2.RemoteEventMessage)
		if !ok {
			continue
		}

		opType := OpReceiveMessage
		if msg.From == lc.Mid {
			opType = OpSendMessage
		}
		bodyText, unwrappedText, decryptionFailed := lc.decryptMessageBody(msg, chatMID, int(opType))
		converted, err := lc.convertLineMessage(ctx, params.Portal, intent, *msg, bodyText, unwrappedText, decryptionFailed)
		if err != nil {
			lc.UserLogin.Bridge.Log.Warn().Err(err).Str("msg_id", msg.ID).Str("chat_mid", chatMID).Msg("Failed to convert message for backfill")
			continue
		}
		if converted == nil {
			continue
		}

		reactions, _ := lc.convertMessageReactions(ctx, msg)
		backfillMsgs = append(backfillMsgs, &bridgev2.BackfillMessage{
			ConvertedMessage: converted,
			Sender:           sender,
			ID:               networkid.MessageID(msg.ID),
			Timestamp:        lc.parseMessageTimestamp(msg),
			Reactions:        reactions,
		})
	}

	var completeCallback func()
	if unblockState != nil {
		completeCallback = unblockState.complete
	}
	return &bridgev2.FetchMessagesResponse{
		Messages:         backfillMsgs,
		HasMore:          false,
		CompleteCallback: completeCallback,
		// Mark the restored chat as read so the silent backfill doesn't leave a
		// stale unread badge — and so the forward batch send never notifies.
		MarkRead: true,
	}, nil
}

func (lc *LineClient) prefetchMessages(ctx context.Context) {
	defer lc.wg.Done()

	opts := line.MessageBoxesOptions{
		ActiveOnly:                     true,
		MessageBoxCountLimit:           messageBoxPageLimit,
		WithUnreadCount:                true,
		LastMessagesPerMessageBoxCount: 0,
	}

	messageBoxes, err := lc.fetchAllMessageBoxes(ctx, opts)
	if err != nil {
		lc.UserLogin.Bridge.Log.Warn().Err(err).Msg("Failed to prefetch message boxes")
		return
	}
	chatMIDs := collectStartupBackfillChatMIDs(messageBoxes, lc.getKnownMemberChatMIDs(), lc.isUserBlocked)

	workerCount := prefetchMessagesConcurrency
	if len(chatMIDs) < workerCount {
		workerCount = len(chatMIDs)
	}
	if workerCount == 0 {
		return
	}

	lc.UserLogin.Bridge.Log.Info().
		Int("message_box_count", len(messageBoxes)).
		Int("chat_count", len(chatMIDs)).
		Int("concurrency", workerCount).
		Msg("Prefetching recent messages")

	jobs := make(chan string)
	var workers sync.WaitGroup
	var backfilledSystemEvents atomic.Bool
	workers.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		go func() {
			defer workers.Done()
			for chatMID := range jobs {
				if ctx.Err() != nil {
					return
				}
				if lc.backfillRecentMessages(ctx, chatMID, startupBackfillMessageLimit) {
					backfilledSystemEvents.Store(true)
				}
			}
		}()
	}

	for _, chatMID := range chatMIDs {
		select {
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return
		case jobs <- chatMID:
		}
	}
	close(jobs)
	workers.Wait()

	// Historical membership/name events are real state events. Replay them in
	// timestamp order, then restore LINE's current authoritative chat state in
	// a hidden resync so a truncated recent-message window cannot leave a room
	// at an old membership or name.
	if backfilledSystemEvents.Load() && ctx.Err() == nil {
		lc.syncChatsNow(ctx)
	}
}

func collectStartupBackfillChatMIDs(messageBoxes []line.MessageBox, memberChatMIDs []string, isBlocked func(string) bool) []string {
	seen := make(map[string]struct{}, len(messageBoxes)+len(memberChatMIDs))
	chatMIDs := make([]string, 0, len(messageBoxes)+len(memberChatMIDs))
	add := func(mid string) {
		if mid == "" {
			return
		}
		if isBlocked != nil && isBlocked(mid) {
			return
		}
		if _, ok := seen[mid]; ok {
			return
		}
		seen[mid] = struct{}{}
		chatMIDs = append(chatMIDs, mid)
	}
	for _, box := range messageBoxes {
		add(box.ID)
	}
	for _, mid := range memberChatMIDs {
		add(mid)
	}
	return chatMIDs
}

// backfillRecentMessages fetches up to limit recent messages for a single
// chat and queues any not already in the local DB through the normal inbound
// (live) message path. Used by prefetchMessages on startup.
func (lc *LineClient) backfillRecentMessages(ctx context.Context, chatMID string, limit int) bool {
	start := time.Now()
	client := lc.newClient()
	msgs, err := client.GetRecentMessagesV2(chatMID, limit)
	if err != nil && lc.shouldAttemptTokenRecovery(ctx, err) {
		if errRecover := lc.recoverToken(ctx); errRecover == nil {
			client = lc.newClient()
			msgs, err = client.GetRecentMessagesV2(chatMID, limit)
		}
	}
	if err != nil {
		lc.UserLogin.Bridge.Log.Warn().Err(err).Str("chat_mid", chatMID).Msg("Failed to fetch recent messages")
		return false
	}
	queued := 0
	skippedExisting := 0
	reactionSyncs := 0
	systemEvents := 0
	// Reverse messages to process oldest first
	for i := len(msgs) - 1; i >= 0; i-- {
		msg := msgs[i]
		lc.cacheGroupMembersFromMessage(chatMID, msg)

		existing, err := lc.UserLogin.Bridge.DB.Message.GetPartByID(ctx, lc.UserLogin.ID, networkid.MessageID(msg.ID), "")
		if err == nil && existing != nil {
			skippedExisting++
			if lc.queueMessageReactionSync(ctx, chatMID, msg) {
				reactionSyncs++
			}
			continue
		} else if err != nil {
			lc.UserLogin.Bridge.Log.Warn().
				Err(err).
				Str("chat_mid", chatMID).
				Str("msg_id", msg.ID).
				Msg("Failed to check whether recent message already exists")
		}

		opType := OpReceiveMessage
		if msg.From == lc.Mid {
			opType = OpSendMessage
		}
		var didQueue bool
		if ContentType(msg.ContentType) == ContentSystem {
			didQueue = lc.queueHistoricalSystemMessage(msg, int(opType))
		} else {
			didQueue = lc.queueIncomingMessage(msg, int(opType))
		}
		if didQueue {
			queued++
			if ContentType(msg.ContentType) == ContentSystem {
				systemEvents++
			}
			if lc.queueMessageReactionSync(ctx, chatMID, msg) {
				reactionSyncs++
			}
		}
	}
	lc.UserLogin.Bridge.Log.Debug().
		Str("chat_mid", chatMID).
		Int("fetched", len(msgs)).
		Int("queued", queued).
		Int("system_events", systemEvents).
		Int("reaction_syncs", reactionSyncs).
		Int("skipped_existing", skippedExisting).
		Dur("duration", time.Since(start)).
		Msg("Finished recent-message backfill")
	return systemEvents > 0
}

func (lc *LineClient) syncChats(ctx context.Context) {
	defer lc.wg.Done()
	lc.syncChatsNow(ctx)
}

func (lc *LineClient) syncChatsNow(ctx context.Context) {
	client := lc.newClient()
	midsResp, err := client.GetAllChatMids(true, true)
	if err != nil && lc.shouldAttemptTokenRecovery(ctx, err) {
		if errRecover := lc.recoverToken(ctx); errRecover == nil {
			client = lc.newClient()
			midsResp, err = client.GetAllChatMids(true, true)
		}
	}
	if err != nil {
		lc.UserLogin.Bridge.Log.Warn().Err(err).Msg("Failed to fetch all chat mids")
		return
	}

	allMids := append(midsResp.MemberChatMids, midsResp.InvitedChatMids...)
	if len(allMids) == 0 {
		lc.setKnownMemberChatMIDs(nil)
		return
	}
	lc.setKnownMemberChatMIDs(midsResp.MemberChatMids)
	memberChatMids := make(map[string]struct{}, len(midsResp.MemberChatMids))
	for _, mid := range midsResp.MemberChatMids {
		memberChatMids[mid] = struct{}{}
	}
	var pendingPortalCreates []*bridgev2.Portal

	chunkSize := 20
	for i := 0; i < len(allMids); i += chunkSize {
		end := i + chunkSize
		if end > len(allMids) {
			end = len(allMids)
		}
		batch := allMids[i:end]
		chatsResp, err := client.GetChats(batch, true, true)
		if err != nil && lc.shouldAttemptTokenRecovery(ctx, err) {
			if errRecover := lc.recoverToken(ctx); errRecover == nil {
				client = lc.newClient()
				chatsResp, err = client.GetChats(batch, true, true)
			}
		}
		if err != nil {
			lc.UserLogin.Bridge.Log.Warn().Err(err).Msg("Failed to fetch batch of chats")
			continue
		}

		for _, chat := range chatsResp.Chats {
			portalKey := networkid.PortalKey{ID: makePortalID(chat.ChatMid), Receiver: lc.UserLogin.ID}

			existingPortal, err := lc.UserLogin.Bridge.GetExistingPortalByKey(ctx, portalKey)
			if err != nil {
				lc.UserLogin.Bridge.Log.Warn().Err(err).Str("chat_mid", chat.ChatMid).Msg("Failed to find existing group portal before sync")
			}

			info := lc.chatToChatInfo(ctx, &chat, true)
			// Existing member groups must have their Matrix portal before prefetch/SSE can
			// deliver messages. If the first message creates the portal, bridgev2 has to
			// join the sender ghost on demand and Beeper shows that old membership as a
			// fresh "joined the chat" event. Invited (not-yet-joined) chats are also
			// created so the pending invite surfaces as a Request.
			createPortal := info.MessageRequest != nil && *info.MessageRequest
			_, isMemberChat := memberChatMids[chat.ChatMid]
			if isMemberChat {
				createPortal = true
			}
			if createPortal && isMemberChat && (existingPortal == nil || existingPortal.MXID == "") {
				lc.stripRemoteMembersFromInitialChatInfo(info)
			}
			lc.UserLogin.Bridge.QueueRemoteEvent(lc.UserLogin, &simplevent.ChatResync{
				EventMeta: simplevent.EventMeta{
					Type:         bridgev2.RemoteEventChatResync,
					PortalKey:    portalKey,
					CreatePortal: createPortal,
					Timestamp:    time.Now(),
				},
				ChatInfo: info,
			})
			if createPortal {
				portal, err := lc.UserLogin.Bridge.GetExistingPortalByKey(ctx, portalKey)
				if err != nil {
					lc.UserLogin.Bridge.Log.Warn().Err(err).Str("chat_mid", chat.ChatMid).Msg("Failed to find group portal after queueing create")
				} else if portal != nil && portal.MXID == "" {
					pendingPortalCreates = append(pendingPortalCreates, portal)
				}
			}
		}
	}

	lc.waitForGroupPortalCreates(ctx, pendingPortalCreates)
}

func (lc *LineClient) waitForGroupPortalCreates(ctx context.Context, portals []*bridgev2.Portal) {
	if len(portals) == 0 {
		return
	}
	waitCtx, cancel := context.WithTimeout(ctx, groupPortalCreateWait)
	defer cancel()
	for _, portal := range portals {
		if portal == nil || portal.MXID != "" || portal.RoomCreated.IsSet() {
			continue
		}
		if err := portal.RoomCreated.Wait(waitCtx); err != nil {
			lc.UserLogin.Bridge.Log.Warn().
				Err(err).
				Object("portal_key", portal.PortalKey).
				Msg("Timed out waiting for startup group portal creation")
			return
		}
	}
}

func (lc *LineClient) setKnownMemberChatMIDs(mids []string) {
	lc.cacheMu.Lock()
	defer lc.cacheMu.Unlock()
	lc.knownMemberChatMIDs = make(map[string]struct{}, len(mids))
	for _, mid := range mids {
		if isChatMID(mid) {
			lc.knownMemberChatMIDs[mid] = struct{}{}
		}
	}
}

func (lc *LineClient) getKnownMemberChatMIDs() []string {
	lc.cacheMu.Lock()
	defer lc.cacheMu.Unlock()
	mids := make([]string, 0, len(lc.knownMemberChatMIDs))
	for mid := range lc.knownMemberChatMIDs {
		mids = append(mids, mid)
	}
	sort.Strings(mids)
	return mids
}

func (lc *LineClient) chatToChatInfo(ctx context.Context, chat *line.Chat, excludeFromTimeline bool) *bridgev2.ChatInfo {
	members := []bridgev2.ChatMember{
		{
			EventSender: bridgev2.EventSender{
				IsFromMe: true,
				Sender:   networkid.UserID(lc.UserLogin.ID),
			},
			Membership: event.MembershipJoin,
			PowerLevel: ptr.Ptr(0),
		},
	}

	var groupMemberMids []string
	selfInvitePending := false
	if chat.Extra.GroupExtra != nil {
		if chat.Extra.GroupExtra.CreatorMid == lc.Mid {
			members[0].PowerLevel = ptr.Ptr(100)
		}
		// If the bridge user is invited but not yet a full member of a GROUP (type 0), surface the
		// chat as a Beeper message request (Requests section) via info.MessageRequest below. The
		// gate is GROUP-only on purpose: LINE ROOMs (type 1) have no accept step — invitees are
		// auto-joined (see the invitee loop below, which also joins type-1 invitees) — so a room is
		// created as a normal joined room rather than a request. Don't mark self as
		// MembershipInvite: on Beeper an invite-membership self user is excluded from the room
		// entirely (getInitialMemberList skips non-join members), so the room would never appear.
		if _, isMember := chat.Extra.GroupExtra.MemberMids[lc.Mid]; !isMember && chat.Type == 0 {
			if _, isInvitee := chat.Extra.GroupExtra.InviteeMids[lc.Mid]; isInvitee {
				selfInvitePending = true
			}
		}

		// Populate group member cache for fallback use when GetChats
		// returns empty MemberMids (known LINE API issue).
		allMemberMids := make([]string, 0, len(chat.Extra.GroupExtra.MemberMids))
		for m := range chat.Extra.GroupExtra.MemberMids {
			if !isUserMID(m) || lc.isOwnMID(m) {
				continue
			}
			allMemberMids = append(allMemberMids, m)
			members = append(members, bridgev2.ChatMember{
				EventSender: bridgev2.EventSender{
					Sender: makeUserID(m),
				},
				Membership:       event.MembershipJoin,
				MemberEventExtra: hiddenMemberEventExtra(excludeFromTimeline),
			})
		}
		for m := range chat.Extra.GroupExtra.InviteeMids {
			if !isUserMID(m) || lc.isOwnMID(m) {
				continue
			}
			allMemberMids = append(allMemberMids, m)
			membership := event.MembershipInvite
			if chat.Type == 1 {
				membership = event.MembershipJoin
			}
			members = append(members, bridgev2.ChatMember{
				EventSender: bridgev2.EventSender{
					Sender: makeUserID(m),
				},
				Membership:       membership,
				MemberEventExtra: hiddenMemberEventExtra(excludeFromTimeline),
			})
		}
		if len(allMemberMids) == 0 {
			lc.cacheGroupMembersFromRecentMessages(ctx, chat.ChatMid)
			for _, m := range lc.getCachedGroupMembers(chat.ChatMid) {
				if !isUserMID(m) || lc.isOwnMID(m) {
					continue
				}
				allMemberMids = append(allMemberMids, m)
				members = append(members, bridgev2.ChatMember{
					EventSender: bridgev2.EventSender{
						Sender: makeUserID(m),
					},
					Membership:       event.MembershipJoin,
					MemberEventExtra: hiddenMemberEventExtra(excludeFromTimeline),
				})
			}
		}

		groupMemberMids = make([]string, 0, len(allMemberMids)+1)
		groupMemberMids = append(groupMemberMids, lc.Mid)
		groupMemberMids = append(groupMemberMids, allMemberMids...)
		lc.cacheGroupMemberMIDs(chat.ChatMid, groupMemberMids)
		lc.cacheMu.Lock()
		if lc.generatedGroupNameCache == nil {
			lc.generatedGroupNameCache = make(map[string]bool)
		}
		lc.cacheMu.Unlock()
	}

	name := chat.ChatName
	if chat.Extra.GroupExtra != nil && chat.Type == 1 {
		lc.cacheMu.Lock()
		generateName := lc.generatedGroupNameCache[chat.ChatMid]
		lc.cacheMu.Unlock()
		if generateName && len(groupMemberMids) > 1 {
			name = lc.generateNameFromMemberList(ctx, groupMemberMids)
		}
	}
	if name == "" && chat.Extra.GroupExtra != nil {
		name = lc.generateNameFromMemberList(ctx, groupMemberMids)
	}

	ct := database.RoomTypeGroupDM
	if chat.Extra.GroupExtra == nil {
		ct = database.RoomTypeDM
	}

	info := &bridgev2.ChatInfo{
		Type:   &ct,
		Name:   &name,
		Avatar: lc.avatarFromPicturePath(chat.PicturePath),
		Members: &bridgev2.ChatMemberList{
			IsFull:                     true,
			MemberMap:                  chatMemberMapFromList(members),
			ExcludeChangesFromTimeline: excludeFromTimeline,
		},
		ExcludeChangesFromTimeline: excludeFromTimeline,
	}
	// Leave MessageRequest nil for non-invite chats so a racing resync can't clear the flag
	// out from under an accept that's already in flight.
	if selfInvitePending {
		info.MessageRequest = ptr.Ptr(true)
	}
	return info
}

func (lc *LineClient) generateNameFromMemberList(ctx context.Context, members []string) string {
	var names []string
	count := 0
	seen := make(map[string]struct{}, len(members))
	for _, mid := range members {
		if mid == string(lc.UserLogin.ID) || mid == lc.Mid || strings.HasPrefix(mid, "c") || strings.HasPrefix(mid, "r") {
			continue
		}
		if _, ok := seen[mid]; ok {
			continue
		}
		seen[mid] = struct{}{}
		contact := lc.getContact(ctx, mid)
		name := contact.EffectiveDisplayName()
		if name != "" && name != mid {
			names = append(names, name)
		}
		count++
		if count >= 20 {
			break
		}
	}

	finalNames := names
	if len(names) > 3 {
		finalNames = names[:3]
	}

	if len(finalNames) == 0 {
		return ""
	}

	result := strings.Join(finalNames, ", ")
	actualMemberCount := 0
	seen = make(map[string]struct{}, len(members))
	for _, m := range members {
		if _, ok := seen[m]; ok {
			continue
		}
		seen[m] = struct{}{}
		if m != string(lc.UserLogin.ID) && !strings.HasPrefix(m, "c") && !strings.HasPrefix(m, "r") {
			actualMemberCount++
		}
	}
	remaining := actualMemberCount - len(finalNames)
	if remaining > 0 {
		result += fmt.Sprintf(" and %d others", remaining)
	}
	return result
}

func (lc *LineClient) getCachedGroupMembers(chatMid string) []string {
	lc.cacheMu.Lock()
	defer lc.cacheMu.Unlock()
	members := lc.groupMemberCache[chatMid]
	if len(members) == 0 {
		return nil
	}
	return append([]string(nil), members...)
}

func (lc *LineClient) cacheGroupMembersFromSystemMessage(msg *line.Message) {
	if msg == nil || msg.ContentMetadata == nil {
		return
	}
	chatMid := msg.To
	if !isChatMID(chatMid) {
		return
	}
	locKey := msg.ContentMetadata["LOC_KEY"]
	switch locKey {
	case "C_GI", "C_MI", "A_MI":
		lc.addGroupMembersToCache(chatMid, midsFromSystemLocArgs(msg.ContentMetadata["LOC_ARGS"])...)
	case "A_MC":
		lc.addGroupMembersToCache(chatMid, append(midsFromSystemLocArgs(msg.ContentMetadata["LOC_ARGS"]), msg.From)...)
	case "C_MJ", "A_MJ":
		lc.addGroupMembersToCache(chatMid, msg.From)
	case "C_ML", "A_ML", "C_MR", "A_MR":
		lc.removeGroupMemberFromCache(chatMid, msg.From)
	case "C_IC":
		parts := strings.SplitN(msg.ContentMetadata["LOC_ARGS"], "\x1e", 2)
		if len(parts) == 2 {
			lc.removeGroupMemberFromCache(chatMid, parts[1])
		}
	}
}

func (lc *LineClient) cacheGroupMembersFromMessage(chatMid string, msg *line.Message) {
	if msg == nil || !isChatMID(chatMid) {
		return
	}
	if ContentType(msg.ContentType) == ContentSystem {
		lc.cacheGroupMembersFromSystemMessage(msg)
		return
	}
	if !isBridgeableContentType(msg) {
		return
	}
	lc.addGroupMembersToCache(chatMid, msg.From)
}

func (lc *LineClient) cacheGroupMembersFromRecentMessages(ctx context.Context, chatMid string) {
	if len(lc.getCachedGroupMembers(chatMid)) > 1 {
		return
	}
	client := lc.newClient()
	msgs, err := client.GetRecentMessagesV2(chatMid, 50)
	if err != nil && lc.shouldAttemptTokenRecovery(ctx, err) {
		if errRecover := lc.recoverToken(ctx); errRecover == nil {
			client = lc.newClient()
			msgs, err = client.GetRecentMessagesV2(chatMid, 50)
		}
	}
	if err != nil {
		lc.UserLogin.Bridge.Log.Debug().Err(err).Str("chat_mid", chatMid).Msg("Failed to fetch recent messages for group member cache")
		return
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		lc.cacheGroupMembersFromMessage(chatMid, msgs[i])
	}
}

func midsFromSystemLocArgs(locArgs string) []string {
	fields := strings.FieldsFunc(locArgs, func(r rune) bool {
		return r == '\x1e' || r == '\x1f'
	})
	mids := make([]string, 0, len(fields))
	for _, field := range fields {
		if isUserMID(field) {
			mids = append(mids, field)
		}
	}
	return mids
}

func isUserMID(mid string) bool {
	return len(mid) > 1 && (mid[0] == 'U' || mid[0] == 'u')
}

func isChatMID(mid string) bool {
	if mid == "" {
		return false
	}
	lower := strings.ToLower(mid)
	return strings.HasPrefix(lower, "c") || strings.HasPrefix(lower, "r")
}

func (lc *LineClient) isOwnMID(mid string) bool {
	if mid == "" {
		return false
	}
	if mid == lc.Mid {
		return true
	}
	if lc.UserLogin != nil && lc.UserLogin.UserLogin != nil && mid == string(lc.UserLogin.ID) {
		return true
	}
	return false
}

func (lc *LineClient) eventSenderForMID(mid string) bridgev2.EventSender {
	return bridgev2.EventSender{
		Sender:   makeUserID(mid),
		IsFromMe: lc.isOwnMID(mid),
	}
}

// resolveReactionSenderMID mirrors the Chrome extension's operation handling:
// SEND_REACTION (type 139) updates the current user's reaction without a sender,
// while NOTIFIED_SEND_REACTION (type 140) always passes param3 as senderMid.
// This handler is shared by DMs and groups regardless of Letter Sealing mode.
// A missing param3 can only be inferred safely in a DM, where chatMid is the
// other user's MID. Never turn a group chat MID into a Matrix ghost.
func (lc *LineClient) resolveReactionSenderMID(opType OperationType, op line.Operation, chatMid string) string {
	if opType == OpPredefinedReaction {
		return string(lc.UserLogin.ID)
	}
	if isUserMID(op.Param3) {
		return op.Param3
	}
	if op.Param3 == "" && isUserMID(chatMid) {
		return chatMid
	}
	return ""
}

func hiddenMemberEventExtra(exclude bool) map[string]any {
	if !exclude {
		return nil
	}
	return map[string]any{beeperExcludeFromTimelineKey: true}
}

func chatMemberMapFromList(members []bridgev2.ChatMember) bridgev2.ChatMemberMap {
	memberMap := make(bridgev2.ChatMemberMap, len(members))
	for _, member := range members {
		memberMap.Set(member)
	}
	return memberMap
}

func (lc *LineClient) selfChatMember() bridgev2.ChatMember {
	return bridgev2.ChatMember{
		EventSender: bridgev2.EventSender{
			IsFromMe: true,
			Sender:   networkid.UserID(lc.UserLogin.ID),
		},
		Membership: event.MembershipJoin,
		PowerLevel: ptr.Ptr(0),
	}
}

func (lc *LineClient) isOwnChatMember(member bridgev2.ChatMember) bool {
	if member.IsFromMe {
		return true
	}
	if lc.UserLogin != nil && member.SenderLogin == lc.UserLogin.ID {
		return true
	}
	return lc.isOwnMID(string(member.Sender))
}

func (lc *LineClient) stripRemoteMembersFromInitialChatInfo(info *bridgev2.ChatInfo) {
	if info == nil || info.Members == nil {
		return
	}

	members := info.Members
	filteredMemberMap := make(bridgev2.ChatMemberMap)
	for userID, member := range members.MemberMap {
		if lc.isOwnChatMember(member) {
			filteredMemberMap[userID] = member
		}
	}
	if len(filteredMemberMap) == 0 {
		filteredMemberMap.Set(lc.selfChatMember())
	}

	filtered := &bridgev2.ChatMemberList{
		IsFull:                     false,
		CheckAllLogins:             members.CheckAllLogins,
		ExcludeChangesFromTimeline: members.ExcludeChangesFromTimeline,
		TotalMemberCount:           members.TotalMemberCount,
		OtherUserID:                members.OtherUserID,
		MemberMap:                  filteredMemberMap,
		PowerLevels:                members.PowerLevels,
	}
	info.Members = filtered
}

func (lc *LineClient) addGroupMembersToCache(chatMid string, mids ...string) bool {
	if !isChatMID(chatMid) {
		return false
	}

	lc.cacheMu.Lock()
	defer lc.cacheMu.Unlock()

	if lc.groupMemberCache == nil {
		lc.groupMemberCache = make(map[string][]string)
	}

	seen := make(map[string]struct{}, len(lc.groupMemberCache[chatMid])+len(mids)+1)
	members := make([]string, 0, len(lc.groupMemberCache[chatMid])+len(mids)+1)
	appendMID := func(mid string) bool {
		if !isUserMID(mid) {
			return false
		}
		if _, ok := seen[mid]; ok {
			return false
		}
		seen[mid] = struct{}{}
		members = append(members, mid)
		return true
	}

	appendMID(lc.Mid)
	if lc.UserLogin != nil {
		appendMID(string(lc.UserLogin.ID))
	}
	for _, mid := range lc.groupMemberCache[chatMid] {
		appendMID(mid)
	}

	added := false
	for _, mid := range mids {
		if appendMID(mid) {
			added = true
		}
	}
	if len(members) > 0 {
		lc.groupMemberCache[chatMid] = members
	}
	return added
}

func (lc *LineClient) removeGroupMemberFromCache(chatMid, mid string) bool {
	if !isChatMID(chatMid) || !isUserMID(mid) {
		return false
	}

	lc.cacheMu.Lock()
	defer lc.cacheMu.Unlock()

	members := lc.groupMemberCache[chatMid]
	if len(members) == 0 {
		return false
	}

	changed := false
	next := members[:0]
	for _, member := range members {
		if member == mid {
			changed = true
			continue
		}
		next = append(next, member)
	}
	if changed {
		lc.groupMemberCache[chatMid] = append([]string(nil), next...)
	}
	return changed
}

func (lc *LineClient) ensureGroupMessageSenderKnown(chatMid, senderMid string, ts time.Time) {
	if !isChatMID(chatMid) || !isUserMID(senderMid) || lc.isOwnMID(senderMid) {
		return
	}
	if !lc.addGroupMembersToCache(chatMid, senderMid) {
		return
	}
	lc.emitMemberChange(chatMid, senderMid, event.MembershipJoin, ts, true)
}

func (lc *LineClient) hiddenJoinGroupMessageSender(ctx context.Context, portal *bridgev2.Portal, chatMid, senderMid string, ts time.Time) {
	if !isChatMID(chatMid) || !isUserMID(senderMid) || lc.isOwnMID(senderMid) {
		return
	}
	lc.addGroupMembersToCache(chatMid, senderMid)
	if portal == nil || portal.MXID == "" {
		return
	}
	portal.ProcessChatInfoChange(ctx, bridgev2.EventSender{}, lc.UserLogin, &bridgev2.ChatInfoChange{
		MemberChanges: &bridgev2.ChatMemberList{
			ExcludeChangesFromTimeline: true,
			Members: []bridgev2.ChatMember{
				{
					EventSender:      bridgev2.EventSender{Sender: makeUserID(senderMid)},
					Membership:       event.MembershipJoin,
					MemberEventExtra: hiddenMemberEventExtra(true),
				},
			},
		},
	}, ts)
}

func (lc *LineClient) refreshGroupsForContact(ctx context.Context, mid string) {
	type groupUpdate struct {
		chatMid       string
		members       []string
		generatedName bool
	}
	var updates []groupUpdate

	lc.cacheMu.Lock()
	for chatMid, members := range lc.groupMemberCache {
		for _, member := range members {
			if member == mid {
				updates = append(updates, groupUpdate{
					chatMid:       chatMid,
					members:       append([]string(nil), members...),
					generatedName: lc.generatedGroupNameCache[chatMid],
				})
				break
			}
		}
	}
	lc.cacheMu.Unlock()

	for _, update := range updates {
		var name *string
		if update.generatedName {
			generatedName := lc.generateNameFromMemberList(ctx, update.members)
			if generatedName != "" {
				name = &generatedName
			}
		}
		if name == nil {
			continue
		}
		portalKey := networkid.PortalKey{ID: makePortalID(update.chatMid), Receiver: lc.UserLogin.ID}
		lc.UserLogin.Bridge.QueueRemoteEvent(lc.UserLogin, &simplevent.ChatInfoChange{
			EventMeta: simplevent.EventMeta{
				Type:      bridgev2.RemoteEventChatInfoChange,
				PortalKey: portalKey,
				Timestamp: time.Now(),
			},
			ChatInfoChange: &bridgev2.ChatInfoChange{
				ChatInfo: &bridgev2.ChatInfo{
					Name: name,
				},
			},
		})
	}
}

func startReceiveAuthProbeContext(parent context.Context, startedAt time.Time) (context.Context, context.CancelFunc, time.Time) {
	if receiveAuthProbeInterval <= 0 {
		return parent, func() {}, time.Time{}
	}

	nextProbeAt := startedAt.Add(receiveAuthProbeInterval)
	if now := receiveAuthProbeNow(); !nextProbeAt.After(now) {
		nextProbeAt = now.Add(receiveAuthProbeInterval)
	}
	receiveCtx, cancel := newReceiveAuthProbeContext(parent, nextProbeAt)
	return receiveCtx, cancel, nextProbeAt
}

func isReceiveAuthProbeDue(receiveCtx context.Context, nextProbeAt time.Time) bool {
	if receiveAuthProbeInterval <= 0 {
		return false
	}
	return errors.Is(context.Cause(receiveCtx), errReceiveAuthProbeDue) || !receiveAuthProbeNow().Before(nextProbeAt)
}

func waitForSSEReconnect(ctx context.Context) bool {
	if sseReconnectDelay <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(sseReconnectDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (lc *LineClient) pollLoop(ctx context.Context) {
	defer lc.wg.Done()

	var localRev int64 = 0
	client := lc.newClient()

	lc.UserLogin.Bridge.Log.Info().Msg("Starting LINE SSE loop...")
	rev, err := getLastOpRevisionWithClient(ctx, client)
	if err != nil && lc.isLoggedOut(err) {
		lc.markLoggedOutByOtherClient(ctx, err)
		return
	}
	if err != nil && lc.shouldAttemptTokenRecovery(ctx, err) {
		if errRecover := lc.recoverToken(ctx); errRecover == nil {
			client = lc.newClient()
			rev, err = getLastOpRevisionWithClient(ctx, client)
		} else {
			lc.UserLogin.Bridge.Log.Warn().Err(errRecover).Msg("Failed to recover token for getLastOpRevision")
		}
	}
	if err != nil {
		lc.UserLogin.Bridge.Log.Warn().Err(err).Msg("Failed to get last op revision")
	} else {
		localRev = rev
		lc.UserLogin.Bridge.Log.Info().Int64("local_rev", localRev).Msg("Seeded local revision from getLastOpRevision")
	}

	receiveCtx, cancelReceive, nextProbeAt := startReceiveAuthProbeContext(ctx, receiveAuthProbeNow())
	defer func() {
		cancelReceive()
	}()
	probeAndReschedule := func() bool {
		probeStartedAt := receiveAuthProbeNow()
		if lc.handleReceiveAuthProbe(ctx) || ctx.Err() != nil {
			return true
		}
		cancelReceive()
		receiveCtx, cancelReceive, nextProbeAt = startReceiveAuthProbeContext(ctx, probeStartedAt)
		return false
	}

	handler := func(eventType, data string) {
		// handle keep alives
		if eventType == "ping" || eventType == "connInfoRevision" {
			return
		}

		// handle fullsync requests
		if eventType == "fullSync" {
			lc.UserLogin.Bridge.Log.Info().Msg("Received fullSync request")

			var fsPayload struct {
				NextRevision string `json:"nextRevision"`
			}
			if err := json.Unmarshal([]byte(data), &fsPayload); err == nil && fsPayload.NextRevision != "" {
				if newRev, err := strconv.ParseInt(fsPayload.NextRevision, 10, 64); err == nil {
					lc.UserLogin.Bridge.Log.Info().Int64("old", localRev).Int64("new", newRev).Msg("Updating local revision from fullSync")

					localRev = newRev

				}
			}
			newlyUnblocked, err := lc.refreshBlockedContacts(ctx)
			if err != nil {
				lc.UserLogin.Bridge.Log.Warn().Err(err).Msg("Failed to refresh blocked contacts during fullSync")
			}
			for _, mid := range newlyUnblocked {
				lc.queueUnblockedDMRestore(ctx, mid, "full_sync")
			}
			lc.wg.Add(3)
			go lc.syncChats(ctx)
			go lc.syncDMChats(ctx)
			go lc.prefetchMessages(ctx)
			return
		}

		// handle operations
		if eventType == "operation" {
			var op line.Operation
			if err := json.Unmarshal([]byte(data), &op); err != nil {
				lc.UserLogin.Bridge.Log.Error().Err(err).Msg("Failed to unmarshal op")
				return
			}

			rev, _ := op.Revision.Int64()
			if rev > localRev {
				localRev = rev
			}

			lc.handleOperation(ctx, op)
		}
	}

	for {
		if ctx.Err() != nil {
			return
		}
		if isReceiveAuthProbeDue(receiveCtx, nextProbeAt) {
			if probeAndReschedule() {
				return
			}
			continue
		}

		client = lc.newClient()
		err := listenSSEWithClient(client, receiveCtx, localRev, handler)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if isReceiveAuthProbeDue(receiveCtx, nextProbeAt) {
				if probeAndReschedule() {
					return
				}
				continue
			}
			if errors.Is(err, context.Canceled) {
				return
			}
			if !errors.Is(err, io.EOF) {
				lc.UserLogin.Bridge.Log.Warn().Err(err).Msg("SSE Disconnected")

				if line.IsSSEIdleTimeout(err) {
					if probeAndReschedule() {
						return
					}
					if !waitForSSEReconnect(receiveCtx) {
						continue
					}
					continue
				}

				if lc.isLoggedOut(err) {
					lc.markLoggedOutByOtherClient(ctx, err)
					return
				}

				if line.IsUnauthorizedStatus(err) {
					if lc.handleReceiveAuthError(ctx, err) {
						return
					}
				}
			}
			if !waitForSSEReconnect(receiveCtx) {
				continue
			}
		}
	}
}

// handleReceiveAuthProbe checks Talk auth independently of SSE activity. The
// caller reconnects from the existing localRev after every probe so operations
// that arrived during the check are replayed rather than skipped.
func (lc *LineClient) handleReceiveAuthProbe(ctx context.Context) bool {
	if ctx.Err() != nil || lc.isSessionInvalidated() {
		return true
	}
	if lc.superseded.Load() {
		return true
	}

	// This is only a health probe. Keep localRev unchanged so the reconnected
	// stream replays operations that arrived while the old stream was stalled.
	_, probeErr := getLastOpRevisionWithClient(ctx, lc.newClient())
	if probeErr == nil {
		return false
	}
	if ctx.Err() != nil {
		return true
	}

	if lc.isLoggedOut(probeErr) {
		lc.markLoggedOutByOtherClient(ctx, probeErr)
		return true
	}

	if line.IsUnauthorizedStatus(probeErr) {
		return lc.handleReceiveAuthError(ctx, probeErr)
	}

	if lc.shouldAttemptTokenRecovery(ctx, probeErr) {
		if errRecover := lc.recoverToken(ctx); errRecover != nil {
			if errors.Is(errRecover, errLineSessionInvalidated) || lc.isLoggedOut(errRecover) {
				lc.markLoggedOutByOtherClient(ctx, errRecover)
				return true
			}
			if lc.UserLogin != nil && lc.UserLogin.Bridge != nil {
				lc.UserLogin.Bridge.Log.Warn().Err(errRecover).Msg("Failed to recover token after receive auth probe")
			}
		}
		return false
	}

	if lc.UserLogin != nil && lc.UserLogin.Bridge != nil {
		lc.UserLogin.Bridge.Log.Warn().Err(probeErr).Msg("Receive auth probe failed; reconnecting SSE")
	}
	return false
}

// handleReceiveAuthError handles auth failures from /operation/receive. The
// receive endpoint may return only a bare 401/403, so probe getProfile to reveal
// the detailed forced-logout envelope before deciding whether recovery is safe.
func (lc *LineClient) handleReceiveAuthError(ctx context.Context, err error) bool {
	if lc.isLoggedOut(err) {
		lc.markLoggedOutByOtherClient(ctx, err)
		return true
	}

	_, profileErr := getProfileWithToken(ctx, lc.getAccessToken())
	if ctx.Err() != nil {
		return true
	}
	if lc.isLoggedOut(profileErr) {
		lc.markLoggedOutByOtherClient(ctx, profileErr)
		return true
	}
	if profileErr == nil {
		return false
	}

	if !lc.shouldAttemptTokenRecovery(ctx, err) {
		return true
	}

	if errRecover := lc.recoverToken(ctx); errRecover != nil {
		if ctx.Err() != nil {
			return true
		}
		if errors.Is(errRecover, errLineSessionInvalidated) || lc.isLoggedOut(errRecover) {
			lc.markLoggedOutByOtherClient(ctx, errRecover)
			return true
		}
		if lc.UserLogin != nil && lc.UserLogin.Bridge != nil {
			lc.UserLogin.Bridge.Log.Error().Err(errRecover).Msg("Failed to recover session, stopping poll loop")
		}
		if lc.UserLogin != nil && lc.UserLogin.BridgeState != nil {
			lc.UserLogin.BridgeState.Send(status.BridgeState{
				StateEvent: status.StateBadCredentials,
				Error:      "line-logged-out",
				Message:    "LINE session was invalidated (logged out by another client). Please re-authenticate the bridge.",
				UserAction: status.UserActionRelogin,
			})
		}
		return true
	}
	return false
}

func (lc *LineClient) handleOperation(ctx context.Context, op line.Operation) {
	opType := OperationType(op.Type)

	if opType == OpPredefinedReaction || opType == OpReaction {
		if lc.consumeSentReqSeq(op.ReqSeq) {
			return
		}
	}

	if opType == OpSendMessage {
		lc.reqSeqMu.Lock()
		_, ok := lc.sentReqSeqs[op.ReqSeq]
		if ok {
			delete(lc.sentReqSeqs, op.ReqSeq)
			lc.reqSeqMu.Unlock()
			return
		}
		lc.reqSeqMu.Unlock()
	}

	switch opType {
	case OpBlockContact:
		mid := op.Param1
		lc.cacheMu.Lock()
		lc.blockedUsers[mid] = true
		lc.cacheMu.Unlock()
		lc.saveBlockedContactsSnapshot(ctx)
		lc.UserLogin.Bridge.Log.Info().Str("mid", mid).Msg("Contact blocked")
		// Block operations should only carry user MIDs; skip if it looks like a group/room
		// to avoid blast-radius deleting a group portal on an unexpected payload.
		lowerMid := strings.ToLower(mid)
		if strings.HasPrefix(lowerMid, "c") || strings.HasPrefix(lowerMid, "r") {
			lc.UserLogin.Bridge.Log.Warn().Str("mid", mid).Msg("OpBlockContact carried non-user MID, skipping portal delete")
			return
		}
		portalKey := networkid.PortalKey{ID: makePortalID(mid), Receiver: lc.UserLogin.ID}
		lc.UserLogin.Bridge.QueueRemoteEvent(lc.UserLogin, &simplevent.ChatDelete{
			EventMeta: simplevent.EventMeta{
				Type:      bridgev2.RemoteEventChatDelete,
				PortalKey: portalKey,
				Timestamp: time.Now(),
			},
			OnlyForMe: true,
		})

	case OpUnblockContact:
		mid := op.Param1
		lc.cacheMu.Lock()
		delete(lc.blockedUsers, mid)
		lc.cacheMu.Unlock()
		lc.saveBlockedContactsSnapshot(ctx)
		lc.UserLogin.Bridge.Log.Info().Str("mid", mid).Msg("Contact unblocked")
		// Reattach the DM portal and, when silent batch sending is available,
		// restore its recent history without notifying for every old message.
		// A blocked contact can't have sent anything new, so notifying on unblock
		// is never useful; without batch sending, restore the empty room instead.
		lc.queueUnblockedDMRestore(ctx, mid, "op_unblock")

	case OpContactUpdate:
		mid := op.Param1
		lc.cacheMu.Lock()
		delete(lc.contactCache, mid)
		lc.cacheMu.Unlock()
		contact := lc.getContact(ctx, mid)
		name := contact.EffectiveDisplayName()
		lc.UserLogin.Bridge.Log.Info().Str("mid", mid).Str("name", name).Msg("Contact updated")
		ghost, err := lc.UserLogin.Bridge.GetGhostByID(ctx, makeUserID(mid))
		if err == nil && ghost != nil {
			ghost.UpdateInfo(ctx, &bridgev2.UserInfo{
				Identifiers: []string{mid},
				Name:        &name,
				Avatar:      lc.avatarFromPicturePath(contact.PicturePath),
			})
		}
		dmType := database.RoomTypeDM
		portalKey := networkid.PortalKey{ID: makePortalID(mid), Receiver: lc.UserLogin.ID}
		lc.UserLogin.Bridge.QueueRemoteEvent(lc.UserLogin, &simplevent.ChatResync{
			EventMeta: simplevent.EventMeta{
				Type:      bridgev2.RemoteEventChatResync,
				PortalKey: portalKey,
				Timestamp: time.Now(),
			},
			ChatInfo: &bridgev2.ChatInfo{
				Type:   &dmType,
				Name:   &name,
				Avatar: lc.avatarFromPicturePath(contact.PicturePath),
			},
		})
		lc.refreshGroupsForContact(ctx, mid)

	case OpDeleteSelfFromChat:
		lc.handleSelfLeave(op.Param1)

	case OpSendChatRemoved:
		lc.reqSeqMu.Lock()
		_, ok := lc.sentReqSeqs[op.ReqSeq]
		if ok {
			delete(lc.sentReqSeqs, op.ReqSeq)
			lc.reqSeqMu.Unlock()
			return
		}
		lc.reqSeqMu.Unlock()
		lc.handleSelfLeave(op.Param1)

	case OpDeleteOtherFromChat:
		lc.handleMemberLeave(op.Param1, op.Param2)

	case OpNotifiedLeaveChat:
		lower1 := strings.ToLower(op.Param1)
		if strings.HasPrefix(lower1, "c") || strings.HasPrefix(lower1, "r") {
			lc.handleMemberLeft(op.Param1, op.Param2)
		} else {
			lc.handleMemberLeft(op.Param2, op.Param1)
		}

	case OpNotifiedJoinChat:
		lc.handleMemberJoin(op.Param1, op.Param2)

	case OpCancelInvitation:
		lc.handleMemberLeave(op.Param1, op.Param3)

	case OpInviteIntoChat, OpNotifiedInviteIntoChat:
		lc.wg.Add(1)
		go func() {
			defer lc.wg.Done()
			lc.handleInvite(context.Background(), op.Param1, OperationType(op.Type))
		}()

	case OpChatUpdate, OpChatUpdate2:
		lc.UserLogin.Bridge.Log.Info().Str("chat_mid", op.Param1).Int("op_type", op.Type).Msg("Received chat update operation")
		lc.wg.Add(1)
		go func() {
			defer lc.wg.Done()
			lc.syncSingleChat(context.Background(), op)
		}()

	case OpReadReceipt:
		portalID := makePortalID(op.Param1)
		senderID := makeUserID(op.Param2)

		ts, _ := op.CreatedTime.Int64()
		lc.UserLogin.Bridge.QueueRemoteEvent(lc.UserLogin, &simplevent.Receipt{
			EventMeta: simplevent.EventMeta{
				Type: bridgev2.RemoteEventReadReceipt,
				PortalKey: networkid.PortalKey{
					ID:       portalID,
					Receiver: lc.UserLogin.ID,
				},
				Timestamp: time.UnixMilli(ts),
				Sender:    lc.eventSenderForMID(string(senderID)),
			},
			ReadUpTo: time.UnixMilli(ts),
		})

	case OpUnsendLocal, OpUnsendRemote:
		chatMid := op.Param1
		msgID := op.Param2
		lc.UserLogin.Bridge.Log.Info().Str("msg_id", msgID).Str("chat_mid", chatMid).Int("op_type", op.Type).Msg("Received unsend operation")

		ts, _ := op.CreatedTime.Int64()
		lc.UserLogin.Bridge.QueueRemoteEvent(lc.UserLogin, &simplevent.MessageRemove{
			EventMeta: simplevent.EventMeta{
				Type:      bridgev2.RemoteEventMessageRemove,
				PortalKey: networkid.PortalKey{ID: makePortalID(chatMid), Receiver: lc.UserLogin.ID},
				Timestamp: time.UnixMilli(ts),
			},
			TargetMessage: networkid.MessageID(msgID),
		})

	case OpPredefinedReaction:
		lc.wg.Add(1)
		go func() {
			defer lc.wg.Done()

			param2, err := line.ParseReactionParam2(op.Param2)
			if err != nil {
				lc.UserLogin.Bridge.Log.Error().Err(err).Msg("Failed to parse predefined reaction param2")
				return
			}

			// Type 139 is the "self" event - sender is always the bridge user.
			op.Param3 = lc.resolveReactionSenderMID(OpPredefinedReaction, op, param2.ChatMid)

			// Curr == nil signals a reaction removal/clear from LINE.
			if param2.Curr == nil {
				lc.UserLogin.Bridge.Log.Debug().Str("msg_id", op.Param1).Str("chat_mid", param2.ChatMid).Msg("Received reaction removal (self)")
				lc.handleReactionRemove(op, param2.ChatMid, []networkid.UserID{makeUserID(string(lc.UserLogin.ID))})
				return
			}

			if param2.Curr.PredefinedReactionType != nil {
				lc.handlePredefinedReaction(ctx, op, param2.ChatMid, param2.Curr.PredefinedReactionType.Val)
			} else if param2.Curr.PaidReactionType != nil {
				lc.handlePaidReaction(ctx, op, param2)
			} else {
				lc.UserLogin.Bridge.Log.Error().Msg("No predefined or paid reaction type found in current")
			}
		}()

	case OpReaction:
		lc.wg.Add(1)
		go func() {
			defer lc.wg.Done()

			param2, err := line.ParseReactionParam2(op.Param2)
			if err != nil {
				lc.UserLogin.Bridge.Log.Error().Err(err).Msg("Failed to parse reaction param2")
				return
			}

			op.Param3 = lc.resolveReactionSenderMID(OpReaction, op, param2.ChatMid)
			if op.Param3 == "" {
				lc.UserLogin.Bridge.Log.Warn().
					Str("msg_id", op.Param1).
					Str("chat_mid", param2.ChatMid).
					Msg("Skipping reaction without a user sender MID")
				return
			}

			// Curr == nil signals a reaction removal/clear from LINE. The
			// payload does not carry the previous reaction type, so we don't
			// know whether the original was predefined or paid. Both variants
			// use the type 140 actor from param3, so the sender is unambiguous.
			if param2.Curr == nil {
				lc.UserLogin.Bridge.Log.Debug().Str("msg_id", op.Param1).Str("chat_mid", param2.ChatMid).Msg("Received reaction removal (other)")
				lc.handleReactionRemove(op, param2.ChatMid, []networkid.UserID{makeUserID(op.Param3)})
				return
			}

			// Handle predefined reactions sent via type 140 operations
			if param2.Curr.PaidReactionType == nil && param2.Curr.PredefinedReactionType != nil {
				lc.handlePredefinedReaction(ctx, op, param2.ChatMid, param2.Curr.PredefinedReactionType.Val)
				return
			}

			if param2.Curr.PaidReactionType == nil {
				lc.UserLogin.Bridge.Log.Error().Msg("No paid reaction type found")
				return
			}

			lc.handlePaidReaction(ctx, op, param2)
		}()

	case OpSendMessage, OpReceiveMessage:
		if op.Message != nil {
			if ContentType(op.Message.ContentType) == ContentSystem {
				lc.handleSystemMessage(op)
			} else {
				lc.queueIncomingMessage(op.Message, op.Type)
			}
		}

	default:
		logEvt := lc.UserLogin.Bridge.Log.Debug().
			Int("op_type", op.Type).
			Str("param1", op.Param1).
			Str("param2", op.Param2).
			Str("param3", op.Param3)
		if op.Message != nil {
			logEvt = logEvt.Str("msg_from", op.Message.From).
				Int("msg_content_type", op.Message.ContentType).
				Interface("msg_metadata", op.Message.ContentMetadata)
		}
		logEvt.Msg("Unhandled SSE operation")
	}
}

func (lc *LineClient) handlePaidReaction(ctx context.Context, op line.Operation, param2 *line.ReactionPayload) {
	ts, _ := op.CreatedTime.Int64()
	reaction, err := lc.convertReaction(
		ctx,
		line.ReactionType{PaidReactionType: param2.Curr.PaidReactionType},
		lc.eventSenderForMID(op.Param3),
		time.UnixMilli(ts),
	)
	if err != nil {
		lc.UserLogin.Bridge.Log.Error().Err(err).Msg("Failed to prepare paid reaction icon")
		return
	}
	lc.UserLogin.Bridge.QueueRemoteEvent(
		lc.UserLogin,
		lc.liveReactionSyncEvent(op, param2.ChatMid, reaction.Sender.Sender, reaction),
	)
}

func (lc *LineClient) handlePredefinedReaction(ctx context.Context, op line.Operation, chatMid string, prt int) {
	ts, _ := op.CreatedTime.Int64()
	reaction, err := lc.convertReaction(
		ctx,
		line.ReactionType{PredefinedReactionType: prt},
		lc.eventSenderForMID(op.Param3),
		time.UnixMilli(ts),
	)
	if err != nil {
		lc.UserLogin.Bridge.Log.Error().Err(err).Int("predefined_reaction_type", prt).Msg("Failed to prepare predefined reaction icon")
		return
	}
	lc.UserLogin.Bridge.QueueRemoteEvent(
		lc.UserLogin,
		lc.liveReactionSyncEvent(op, chatMid, reaction.Sender.Sender, reaction),
	)
}

func (lc *LineClient) liveReactionSyncEvent(
	op line.Operation,
	chatMid string,
	sender networkid.UserID,
	reaction *bridgev2.BackfillReaction,
) *simplevent.ReactionSync {
	ts, _ := op.CreatedTime.Int64()
	reactions := []*bridgev2.BackfillReaction(nil)
	if reaction != nil {
		reactions = []*bridgev2.BackfillReaction{reaction}
	}
	return &simplevent.ReactionSync{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventReactionSync,
			PortalKey: networkid.PortalKey{ID: makePortalID(chatMid), Receiver: lc.UserLogin.ID},
			Timestamp: time.UnixMilli(ts),
		},
		TargetMessage: networkid.MessageID(op.Param1),
		Reactions: &bridgev2.ReactionSyncData{
			Users: map[networkid.UserID]*bridgev2.ReactionSyncUser{
				sender: {
					Reactions:       reactions,
					HasAllReactions: true,
				},
			},
			HasAllUsers: false,
		},
	}
}

// handleReactionRemove queues an authoritative empty reaction sync for each
// candidate sender. LINE only allows one reaction per sender, so this removes
// both legacy empty-ID rows and stable paid/predefined reaction IDs without
// needing the previous reaction type.
func (lc *LineClient) handleReactionRemove(op line.Operation, chatMid string, senders []networkid.UserID) {
	for _, sender := range senders {
		lc.UserLogin.Bridge.QueueRemoteEvent(
			lc.UserLogin,
			lc.liveReactionSyncEvent(op, chatMid, sender, nil),
		)
	}
}

func (lc *LineClient) syncSingleChat(ctx context.Context, op line.Operation) {
	chatMid := op.Param1
	client := lc.newClient()
	chatsResp, err := client.GetChats([]string{chatMid}, true, true)
	if err != nil && lc.shouldAttemptTokenRecovery(ctx, err) {
		if errRecover := lc.recoverToken(ctx); errRecover == nil {
			client = lc.newClient()
			chatsResp, err = client.GetChats([]string{chatMid}, true, true)
		}
	}
	if err != nil {
		lc.UserLogin.Bridge.Log.Warn().Err(err).Str("chat_mid", chatMid).Msg("Failed to fetch chat info")
		// Only emit leave if we confirm the user is definitively not a member
		if line.IsNotAMemberError(err) {
			// Confirm via GetAllChatMids before emitting leave
			isMember, isInvitee := lc.checkChatMembership(ctx, chatMid)
			if !isMember && !isInvitee {
				lc.UserLogin.Bridge.Log.Info().Str("chat_mid", chatMid).Msg("Confirmed user not in chat, emitting leave")
				lc.handleSelfLeave(chatMid)
			} else if isInvitee {
				lc.UserLogin.Bridge.Log.Info().Str("chat_mid", chatMid).Msg("User is an invitee, handling invite")
				lc.handleInviteForSelf(ctx, chatMid)
			}
		}
		return
	}
	if len(chatsResp.Chats) == 0 {
		// Chat not returned — verify before emitting leave
		isMember, isInvitee := lc.checkChatMembership(ctx, chatMid)
		if !isMember && !isInvitee {
			lc.UserLogin.Bridge.Log.Info().Str("chat_mid", chatMid).Msg("Chat no longer available, user removed, emitting leave")
			lc.handleSelfLeave(chatMid)
		} else if isInvitee {
			lc.UserLogin.Bridge.Log.Info().Str("chat_mid", chatMid).Msg("User is an invitee (empty resp), handling invite")
			lc.handleInviteForSelf(ctx, chatMid)
		}
		return
	}
	chat := chatsResp.Chats[0]

	portalKey := networkid.PortalKey{ID: makePortalID(chat.ChatMid), Receiver: lc.UserLogin.ID}

	// Use ChatInfoChange to only update avatar (and other non-name metadata).
	// Name updates are handled by handleGroupRename from contentType=18 messages,
	// which has the correct new name from LOC_ARGS.
	// No sender is set on either event to avoid ghost creation/invite issues.
	lc.UserLogin.Bridge.QueueRemoteEvent(lc.UserLogin, &simplevent.ChatInfoChange{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventChatInfoChange,
			PortalKey: portalKey,
			Timestamp: time.Now(),
		},
		ChatInfoChange: &bridgev2.ChatInfoChange{
			ChatInfo: &bridgev2.ChatInfo{
				Avatar: lc.avatarFromPicturePath(chat.PicturePath),
			},
		},
	})
}

// checkChatMembership calls GetAllChatMids to verify whether the bridge user
// is a member or invitee of the given chat.
func (lc *LineClient) checkChatMembership(ctx context.Context, chatMid string) (isMember, isInvitee bool) {
	client := lc.newClient()
	midsResp, err := client.GetAllChatMids(true, true)
	if err != nil && lc.shouldAttemptTokenRecovery(ctx, err) {
		if errRecover := lc.recoverToken(ctx); errRecover == nil {
			client = lc.newClient()
			midsResp, err = client.GetAllChatMids(true, true)
		}
	}
	if err != nil {
		lc.UserLogin.Bridge.Log.Warn().Err(err).Msg("checkChatMembership: GetAllChatMids failed")
		return false, false
	}
	for _, mid := range midsResp.MemberChatMids {
		if mid == chatMid {
			return true, false
		}
	}
	for _, mid := range midsResp.InvitedChatMids {
		if mid == chatMid {
			return false, true
		}
	}
	return false, false
}

func (lc *LineClient) emitMemberChange(chatMid, userMid string, membership event.Membership, ts time.Time, excludeFromTimeline ...bool) {
	lc.emitMemberChangeWithSender(chatMid, userMid, membership, ts, bridgev2.EventSender{}, excludeFromTimeline...)
}

func (lc *LineClient) emitMemberChangeWithSender(
	chatMid, userMid string,
	membership event.Membership,
	ts time.Time,
	changeSender bridgev2.EventSender,
	excludeFromTimeline ...bool,
) {
	exclude := len(excludeFromTimeline) > 0 && excludeFromTimeline[0]
	portalKey := networkid.PortalKey{ID: makePortalID(chatMid), Receiver: lc.UserLogin.ID}
	member := lc.eventSenderForMID(userMid)
	lc.UserLogin.Bridge.QueueRemoteEvent(lc.UserLogin, makeMemberChangeEvent(
		portalKey,
		member,
		changeSender,
		membership,
		ts,
		exclude,
	))
}

func makeMemberChangeEvent(
	portalKey networkid.PortalKey,
	member, changeSender bridgev2.EventSender,
	membership event.Membership,
	ts time.Time,
	exclude bool,
) *simplevent.ChatInfoChange {
	return &simplevent.ChatInfoChange{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventChatInfoChange,
			PortalKey: portalKey,
			Sender:    changeSender,
			Timestamp: ts,
		},
		ChatInfoChange: &bridgev2.ChatInfoChange{
			MemberChanges: &bridgev2.ChatMemberList{
				ExcludeChangesFromTimeline: exclude,
				MemberMap: bridgev2.ChatMemberMap{
					member.Sender: {
						EventSender:      member,
						Membership:       membership,
						MemberEventExtra: hiddenMemberEventExtra(exclude),
					},
				},
			},
		},
	}
}

func (lc *LineClient) handleSelfLeave(chatMid string) {
	selfID := string(lc.UserLogin.ID)
	lc.handleSelfLeaveWithSender(chatMid, lc.eventSenderForMID(selfID))
}

func (lc *LineClient) handleSelfLeaveWithSender(chatMid string, changeSender bridgev2.EventSender) {
	lc.cacheMu.Lock()
	delete(lc.groupMemberCache, chatMid)
	lc.cacheMu.Unlock()
	lc.emitMemberChangeWithSender(
		chatMid,
		string(lc.UserLogin.ID),
		event.MembershipLeave,
		time.Now(),
		changeSender,
	)
}

func (lc *LineClient) handleMemberLeave(chatMid, leaverMid string) {
	lc.handleMemberLeaveWithSender(chatMid, leaverMid, bridgev2.EventSender{})
}

func (lc *LineClient) handleMemberLeft(chatMid, leaverMid string) {
	lc.handleMemberLeaveWithSender(chatMid, leaverMid, lc.eventSenderForMID(leaverMid))
}

func (lc *LineClient) handleMemberLeaveWithSender(chatMid, leaverMid string, changeSender bridgev2.EventSender) {
	lower := strings.ToLower(chatMid)
	if !strings.HasPrefix(lower, "c") && !strings.HasPrefix(lower, "r") {
		return
	}
	if leaverMid == lc.Mid || leaverMid == string(lc.UserLogin.ID) {
		lc.handleSelfLeaveWithSender(chatMid, changeSender)
		return
	}
	lc.removeGroupMemberFromCache(chatMid, leaverMid)
	lc.emitMemberChangeWithSender(chatMid, leaverMid, event.MembershipLeave, time.Now(), changeSender)
}

func (lc *LineClient) handleMemberJoin(chatMid, joinerMid string) {
	lower := strings.ToLower(chatMid)
	if !strings.HasPrefix(lower, "c") && !strings.HasPrefix(lower, "r") {
		return
	}
	lc.addGroupMembersToCache(chatMid, joinerMid)
	lc.emitMemberChange(chatMid, joinerMid, event.MembershipJoin, time.Now())
}

func (lc *LineClient) handleInvite(ctx context.Context, chatMid string, opType OperationType) {
	client := lc.newClient()
	chatsResp, err := client.GetChats([]string{chatMid}, true, true)
	if err != nil && lc.shouldAttemptTokenRecovery(ctx, err) {
		if errRecover := lc.recoverToken(ctx); errRecover == nil {
			client = lc.newClient()
			chatsResp, err = client.GetChats([]string{chatMid}, true, true)
		}
	}
	if err != nil {
		lc.UserLogin.Bridge.Log.Warn().Err(err).Str("chat_mid", chatMid).Msg("Failed to fetch chat info for invite")
		return
	}
	if len(chatsResp.Chats) == 0 || chatsResp.Chats[0].Extra.GroupExtra == nil {
		return
	}
	chat := chatsResp.Chats[0]

	// Both OpInviteIntoChat (123) and OpNotifiedInviteIntoChat (124) dispatch here. We deliberately
	// don't branch on the op number: the op→party mapping is ambiguous and GetChats sometimes omits
	// the caller from the member/invitee lists. Instead, treat the bridge user as the invitee unless
	// they're a confirmed member — a member receiving this op is the inviter or an existing member,
	// whose chat must NOT be flipped into a request. The !member check also covers the LINE quirk
	// where GetChats omits the caller entirely when they are the one being invited.
	_, selfIsMember := chat.Extra.GroupExtra.MemberMids[lc.Mid]
	lc.UserLogin.Bridge.Log.Debug().
		Int("op_type", int(opType)).
		Str("chat_mid", chatMid).
		Bool("self_is_member", selfIsMember).
		Msg("Handling chat invite")
	if !selfIsMember {
		// Bridge user was invited: create the portal as a Beeper message request.
		lc.handleInviteForSelfFromChat(ctx, &chat)
	}

	// Reflect any other pending invitees as invited members of the (now existing) portal.
	membership := event.MembershipInvite
	if chat.Type == 1 {
		membership = event.MembershipJoin
	}
	for inviteeMid := range chat.Extra.GroupExtra.InviteeMids {
		if inviteeMid == lc.Mid || inviteeMid == string(lc.UserLogin.ID) {
			continue
		}
		lc.emitMemberChange(chat.ChatMid, inviteeMid, membership, time.Now())
	}
}

func (lc *LineClient) handleInviteForSelf(ctx context.Context, chatMid string) {
	client := lc.newClient()
	chatsResp, err := client.GetChats([]string{chatMid}, true, true)
	if err != nil && lc.shouldAttemptTokenRecovery(ctx, err) {
		if errRecover := lc.recoverToken(ctx); errRecover == nil {
			client = lc.newClient()
			chatsResp, err = client.GetChats([]string{chatMid}, true, true)
		}
	}
	if err != nil {
		lc.UserLogin.Bridge.Log.Warn().Err(err).Str("chat_mid", chatMid).Msg("Failed to fetch invited chat info")
		return
	}
	if len(chatsResp.Chats) == 0 {
		return
	}
	lc.handleInviteForSelfFromChat(ctx, &chatsResp.Chats[0])
}

// handleInviteForSelfFromChat creates (or resyncs) the portal for a chat the bridge user has
// been invited to, flagged as a Beeper message request via chatToChatInfo.
func (lc *LineClient) handleInviteForSelfFromChat(ctx context.Context, chat *line.Chat) {
	// The bridge user is always the invitee here. Even if GetChats didn't return the bridge
	// user in InviteeMids (which happens when the LINE API doesn't include the caller in the
	// invitee list), we add them so chatToChatInfo flags the chat as a message request.
	if chat.Extra.GroupExtra != nil {
		if chat.Extra.GroupExtra.InviteeMids == nil {
			chat.Extra.GroupExtra.InviteeMids = make(line.FlexibleMidMap)
		}
		chat.Extra.GroupExtra.InviteeMids[lc.Mid] = true
		// Remove from MemberMids just in case, so the message-request flag takes precedence.
		delete(chat.Extra.GroupExtra.MemberMids, lc.Mid)
	}

	portalKey := networkid.PortalKey{ID: makePortalID(chat.ChatMid), Receiver: lc.UserLogin.ID}
	info := lc.chatToChatInfo(ctx, chat, false)
	lc.UserLogin.Bridge.QueueRemoteEvent(lc.UserLogin, &simplevent.ChatResync{
		EventMeta: simplevent.EventMeta{
			Type:         bridgev2.RemoteEventChatResync,
			PortalKey:    portalKey,
			CreatePortal: true,
			Timestamp:    time.Now(),
		},
		ChatInfo: info,
	})
}

func isHandledSystemMessage(msg *line.Message) bool {
	if msg == nil || ContentType(msg.ContentType) != ContentSystem || msg.ContentMetadata == nil || !isChatMID(msg.To) {
		return false
	}
	switch msg.ContentMetadata["LOC_KEY"] {
	case "C_PN":
		parts := strings.SplitN(msg.ContentMetadata["LOC_ARGS"], "\x1e", 2)
		return len(parts) == 2 && parts[1] != ""
	case "C_MJ", "A_MJ", "C_ML", "A_ML", "C_MR", "A_MR", "A_MC":
		return msg.From != ""
	case "C_GI", "C_MI", "A_MI", "C_IC":
		parts := strings.SplitN(msg.ContentMetadata["LOC_ARGS"], "\x1e", 2)
		return len(parts) == 2 && parts[1] != ""
	default:
		return false
	}
}

// makeSystemMessageEvent converts a LINE contentType=18 record into the same
// chat info event used by the live SSE path. A nil event with handled=true is
// the special self-invite case, which schedules its existing portal fallback.
func (lc *LineClient) makeSystemMessageEvent(op line.Operation) (*simplevent.ChatInfoChange, bool) {
	msg := op.Message
	if !isHandledSystemMessage(msg) {
		return nil, false
	}

	locKey := msg.ContentMetadata["LOC_KEY"]
	ts := lc.parseMessageTimestamp(msg)
	portalKey := networkid.PortalKey{ID: makePortalID(msg.To), Receiver: lc.UserLogin.ID}
	switch locKey {
	case "C_PN":
		parts := strings.SplitN(msg.ContentMetadata["LOC_ARGS"], "\x1e", 2)
		newName := parts[1]
		lc.UserLogin.Bridge.Log.Debug().
			Str("new_name", newName).
			Str("chat_mid", msg.To).
			Str("from", msg.From).
			Msg("Handling group rename")
		return &simplevent.ChatInfoChange{
			EventMeta: simplevent.EventMeta{
				Type:      bridgev2.RemoteEventChatInfoChange,
				PortalKey: portalKey,
				Timestamp: ts,
			},
			ChatInfoChange: &bridgev2.ChatInfoChange{
				ChatInfo: &bridgev2.ChatInfo{Name: &newName},
			},
		}, true
	case "C_MJ", "A_MJ":
		lc.addGroupMembersToCache(msg.To, msg.From)
		return makeMemberChangeEvent(
			portalKey,
			lc.eventSenderForMID(msg.From),
			bridgev2.EventSender{},
			event.MembershipJoin,
			ts,
			false,
		), true
	case "C_ML", "A_ML":
		lc.UserLogin.Bridge.Log.Debug().Str("loc_key", locKey).Str("chat_mid", msg.To).Str("leaver_mid", msg.From).Msg("System message: member leave")
		lc.removeGroupMemberFromCache(msg.To, msg.From)
		leaver := lc.eventSenderForMID(msg.From)
		return makeMemberChangeEvent(portalKey, leaver, leaver, event.MembershipLeave, ts, false), true
	case "C_MR", "A_MR":
		lc.UserLogin.Bridge.Log.Debug().Str("loc_key", locKey).Str("chat_mid", msg.To).Str("removed_mid", msg.From).Msg("System message: member removed")
		lc.removeGroupMemberFromCache(msg.To, msg.From)
		return makeMemberChangeEvent(
			portalKey,
			lc.eventSenderForMID(msg.From),
			bridgev2.EventSender{},
			event.MembershipLeave,
			ts,
			false,
		), true
	case "C_GI", "C_MI", "A_MI":
		// msg.From is the inviter, not the invitee. LOC_ARGS has the
		// format inviterMid\x1einviteeMid.
		parts := strings.SplitN(msg.ContentMetadata["LOC_ARGS"], "\x1e", 2)
		inviteeMID := parts[1]
		if lc.isOwnMID(inviteeMID) {
			// Defense-in-depth in case no invite operation arrives. Only do
			// the network fallback if a portal does not already exist.
			chatMID := msg.To
			lc.wg.Add(1)
			go func() {
				defer lc.wg.Done()
				key := networkid.PortalKey{ID: makePortalID(chatMID), Receiver: lc.UserLogin.ID}
				if portal, err := lc.UserLogin.Bridge.GetExistingPortalByKey(context.Background(), key); err == nil && portal != nil && portal.MXID != "" {
					return
				}
				lc.handleInviteForSelf(context.Background(), chatMID)
			}()
			return nil, true
		}
		return makeMemberChangeEvent(
			portalKey,
			lc.eventSenderForMID(inviteeMID),
			bridgev2.EventSender{},
			event.MembershipInvite,
			ts,
			false,
		), true
	case "C_IC":
		// Invitation cancelled. LOC_ARGS has the format
		// cancellerMid\x1einviteeMid.
		parts := strings.SplitN(msg.ContentMetadata["LOC_ARGS"], "\x1e", 2)
		inviteeMID := parts[1]
		lc.removeGroupMemberFromCache(msg.To, inviteeMID)
		return makeMemberChangeEvent(
			portalKey,
			lc.eventSenderForMID(inviteeMID),
			bridgev2.EventSender{},
			event.MembershipLeave,
			ts,
			false,
		), true
	case "A_MC":
		// Auto-join via call / member added. msg.From is the person added.
		lc.addGroupMembersToCache(msg.To, msg.From)
		return makeMemberChangeEvent(
			portalKey,
			lc.eventSenderForMID(msg.From),
			bridgev2.EventSender{},
			event.MembershipJoin,
			ts,
			false,
		), true
	default:
		return nil, false
	}
}

func (lc *LineClient) handleSystemMessage(op line.Operation) bool {
	systemEvent, handled := lc.makeSystemMessageEvent(op)
	if systemEvent != nil {
		lc.UserLogin.Bridge.QueueRemoteEvent(lc.UserLogin, systemEvent)
	} else if !handled && op.Message != nil {
		lc.UserLogin.Bridge.Log.Debug().
			Str("loc_key", op.Message.ContentMetadata["LOC_KEY"]).
			Str("chat_mid", op.Message.To).
			Msg("Unhandled system message LOC_KEY")
	}
	return handled
}

// queueHistoricalSystemMessage persists an invisible marker under the LINE
// message ID. The marker gives startup backfill normal database deduplication,
// while its converter applies the historical state event synchronously before
// the marker is inserted.
func (lc *LineClient) queueHistoricalSystemMessage(msg *line.Message, opType int) bool {
	if !isHandledSystemMessage(msg) {
		lc.UserLogin.Bridge.Log.Debug().
			Str("msg_id", msg.ID).
			Str("loc_key", msg.ContentMetadata["LOC_KEY"]).
			Msg("Skipping unsupported historical system message")
		return false
	}

	op := line.Operation{Type: opType, Message: msg, CreatedTime: msg.CreatedTime}
	portalKey := networkid.PortalKey{ID: makePortalID(msg.To), Receiver: lc.UserLogin.ID}
	result := lc.UserLogin.Bridge.QueueRemoteEvent(lc.UserLogin, &simplevent.Message[line.Operation]{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventMessage,
			PortalKey: portalKey,
			Timestamp: lc.parseMessageTimestamp(msg),
			LogContext: func(c zerolog.Context) zerolog.Context {
				return c.Str("msg_id", msg.ID).Str("loc_key", msg.ContentMetadata["LOC_KEY"])
			},
		},
		Data: op,
		ID:   networkid.MessageID(msg.ID),
		ConvertMessageFunc: func(ctx context.Context, portal *bridgev2.Portal, _ bridgev2.MatrixAPI, data line.Operation) (*bridgev2.ConvertedMessage, error) {
			systemEvent, handled := lc.makeSystemMessageEvent(data)
			if !handled {
				return nil, bridgev2.ErrIgnoringRemoteEvent
			}
			if systemEvent != nil {
				change, err := systemEvent.GetChatInfoChange(ctx)
				if err != nil {
					return nil, fmt.Errorf("get historical system message change: %w", err)
				}
				portal.ProcessChatInfoChange(ctx, systemEvent.GetSender(), lc.UserLogin, change, systemEvent.GetTimestamp())
			}
			return &bridgev2.ConvertedMessage{
				Parts: []*bridgev2.ConvertedMessagePart{{
					Type: event.EventMessage,
					Content: &event.MessageEventContent{
						MsgType:  event.MsgNotice,
						Mentions: &event.Mentions{},
					},
					DontBridge: true,
				}},
			}, nil
		},
	})
	return result.Success && !result.Ignored
}
