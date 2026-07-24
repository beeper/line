package connector

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

func TestMakeMemberChangeEventPreservesChangeSender(t *testing.T) {
	portalKey := networkid.PortalKey{
		ID:       "C-group",
		Receiver: "U-login",
	}
	member := bridgev2.EventSender{Sender: "U-member"}
	timestamp := time.UnixMilli(1_784_890_442_000)

	tests := []struct {
		name         string
		changeSender bridgev2.EventSender
	}{
		{
			name:         "voluntary leave is sent by departing member",
			changeSender: member,
		},
		{
			name:         "removal keeps default bridge sender",
			changeSender: bridgev2.EventSender{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			evt := makeMemberChangeEvent(
				portalKey,
				member,
				tc.changeSender,
				event.MembershipLeave,
				timestamp,
				false,
			)

			if evt.EventMeta.Sender != tc.changeSender {
				t.Fatalf("change sender = %#v, want %#v", evt.EventMeta.Sender, tc.changeSender)
			}
			if !evt.EventMeta.Timestamp.Equal(timestamp) {
				t.Fatalf("timestamp = %s, want %s", evt.EventMeta.Timestamp, timestamp)
			}
			if changes := evt.ChatInfoChange.MemberChanges.MemberMap; len(changes) != 1 {
				t.Fatalf("member changes = %d, want 1", len(changes))
			} else if change, ok := changes[member.Sender]; !ok {
				t.Fatalf("member change for %q not found", member.Sender)
			} else if change.EventSender != member || change.Membership != event.MembershipLeave {
				t.Fatalf("member change = %#v, want member %#v to leave", change, member)
			}
		})
	}
}

func TestMakeSystemMessageEventForHistoricalMembership(t *testing.T) {
	lc := &LineClient{
		Mid: "Uself",
		UserLogin: &bridgev2.UserLogin{
			UserLogin: &database.UserLogin{ID: "Uself"},
			Bridge:    &bridgev2.Bridge{Log: zerolog.Nop()},
		},
	}
	timestamp := time.UnixMilli(1784930400123)

	invite, handled := lc.makeSystemMessageEvent(line.Operation{Message: &line.Message{
		ID:          "invite-message",
		From:        "Uinviter",
		To:          "Cgroup",
		ContentType: int(ContentSystem),
		CreatedTime: json.Number("1784930400123"),
		ContentMetadata: map[string]string{
			"LOC_KEY":  "C_MI",
			"LOC_ARGS": "Uinviter\x1eUinvitee",
		},
	}})
	if !handled || invite == nil {
		t.Fatalf("invite handled/event = %v/%#v, want true/non-nil", handled, invite)
	}
	if !invite.EventMeta.Timestamp.Equal(timestamp) {
		t.Fatalf("invite timestamp = %s, want %s", invite.EventMeta.Timestamp, timestamp)
	}
	inviteChange, ok := invite.ChatInfoChange.MemberChanges.MemberMap["Uinvitee"]
	if !ok || inviteChange.Membership != event.MembershipInvite {
		t.Fatalf("invite member change = %#v", invite.ChatInfoChange.MemberChanges.MemberMap)
	}

	leave, handled := lc.makeSystemMessageEvent(line.Operation{Message: &line.Message{
		ID:          "leave-message",
		From:        "Uleaver",
		To:          "Cgroup",
		ContentType: int(ContentSystem),
		CreatedTime: json.Number("1784930400123"),
		ContentMetadata: map[string]string{
			"LOC_KEY": "C_ML",
		},
	}})
	if !handled || leave == nil {
		t.Fatalf("leave handled/event = %v/%#v, want true/non-nil", handled, leave)
	}
	if leave.EventMeta.Sender.Sender != "Uleaver" {
		t.Fatalf("voluntary leave sender = %q, want Uleaver", leave.EventMeta.Sender.Sender)
	}
	leaveChange, ok := leave.ChatInfoChange.MemberChanges.MemberMap["Uleaver"]
	if !ok || leaveChange.Membership != event.MembershipLeave {
		t.Fatalf("leave member change = %#v", leave.ChatInfoChange.MemberChanges.MemberMap)
	}
}

func TestHandledSystemMessageRejectsUnsupportedRecords(t *testing.T) {
	tests := []struct {
		name string
		msg  *line.Message
	}{
		{name: "nil", msg: nil},
		{
			name: "non-system content",
			msg: &line.Message{
				To:              "Cgroup",
				ContentType:     int(ContentText),
				ContentMetadata: map[string]string{"LOC_KEY": "C_ML"},
			},
		},
		{
			name: "unknown location key",
			msg: &line.Message{
				To:              "Cgroup",
				ContentType:     int(ContentSystem),
				ContentMetadata: map[string]string{"LOC_KEY": "C_UNKNOWN"},
			},
		},
		{
			name: "malformed invite",
			msg: &line.Message{
				To:              "Cgroup",
				ContentType:     int(ContentSystem),
				ContentMetadata: map[string]string{"LOC_KEY": "C_MI", "LOC_ARGS": "Uinviter"},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if isHandledSystemMessage(test.msg) {
				t.Fatal("record was unexpectedly accepted as a handled system message")
			}
		})
	}
}

func installManualReceiveAuthProbeDeadline(t *testing.T) <-chan context.CancelCauseFunc {
	t.Helper()
	oldInterval := receiveAuthProbeInterval
	oldNow := receiveAuthProbeNow
	oldNewContext := newReceiveAuthProbeContext
	t.Cleanup(func() {
		receiveAuthProbeInterval = oldInterval
		receiveAuthProbeNow = oldNow
		newReceiveAuthProbeContext = oldNewContext
	})

	fixedNow := time.Unix(1_700_000_000, 0)
	receiveAuthProbeInterval = 150 * time.Second
	receiveAuthProbeNow = func() time.Time { return fixedNow }
	cancels := make(chan context.CancelCauseFunc, 8)
	newReceiveAuthProbeContext = func(parent context.Context, _ time.Time) (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancelCause(parent)
		cancels <- cancel
		return ctx, func() { cancel(context.Canceled) }
	}
	return cancels
}

func TestDefaultReceiveAuthProbeIntervalIs150Seconds(t *testing.T) {
	if defaultReceiveAuthProbeInterval != 150*time.Second {
		t.Fatalf("default receive auth probe interval = %s, want 150s", defaultReceiveAuthProbeInterval)
	}
	oldInterval := receiveAuthProbeInterval
	oldNow := receiveAuthProbeNow
	oldNewContext := newReceiveAuthProbeContext
	t.Cleanup(func() {
		receiveAuthProbeInterval = oldInterval
		receiveAuthProbeNow = oldNow
		newReceiveAuthProbeContext = oldNewContext
	})

	startedAt := time.Unix(1_700_000_000, 0)
	receiveAuthProbeInterval = defaultReceiveAuthProbeInterval
	receiveAuthProbeNow = func() time.Time { return startedAt }
	var capturedDeadline time.Time
	newReceiveAuthProbeContext = func(parent context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
		capturedDeadline = deadline
		return context.WithCancel(parent)
	}
	_, cancel, nextProbeAt := startReceiveAuthProbeContext(context.Background(), startedAt)
	cancel()

	want := startedAt.Add(150 * time.Second)
	if !capturedDeadline.Equal(want) || !nextProbeAt.Equal(want) {
		t.Fatalf("probe deadline = %s / %s, want %s", capturedDeadline, nextProbeAt, want)
	}
}

func TestUnblockFallbackUsesSilentBackfill(t *testing.T) {
	oldRunSilentUnblockBackfill := runSilentUnblockBackfill
	t.Cleanup(func() {
		runSilentUnblockBackfill = oldRunSilentUnblockBackfill
	})

	called := make(chan string, 1)
	runSilentUnblockBackfill = func(_ context.Context, _ *LineClient, mid string) {
		called <- mid
	}

	log := zerolog.New(io.Discard)
	lc := &LineClient{
		UserLogin: &bridgev2.UserLogin{
			Bridge: &bridgev2.Bridge{Log: log},
		},
	}

	lc.runUnblockBackfillFallback(context.Background(), "U-contact")

	select {
	case mid := <-called:
		if mid != "U-contact" {
			t.Fatalf("silent fallback mid = %q, want U-contact", mid)
		}
	default:
		t.Fatal("silent unblock fallback was not called")
	}
}

func TestUnblockFallbackSkipsSilentBackfillWhenContactIsBlockedAgain(t *testing.T) {
	oldRunSilentUnblockBackfill := runSilentUnblockBackfill
	t.Cleanup(func() {
		runSilentUnblockBackfill = oldRunSilentUnblockBackfill
	})

	called := false
	runSilentUnblockBackfill = func(context.Context, *LineClient, string) {
		called = true
	}

	log := zerolog.New(io.Discard)
	lc := &LineClient{
		UserLogin: &bridgev2.UserLogin{
			Bridge: &bridgev2.Bridge{Log: log},
		},
		blockedUsers: map[string]bool{"U-contact": true},
	}

	lc.runUnblockBackfillFallback(context.Background(), "U-contact")

	if called {
		t.Fatal("silent unblock fallback ran after the contact was blocked again")
	}
}

func TestWaitForUnblockBackfillPortalRetriesUntilReady(t *testing.T) {
	readyPortal := &bridgev2.Portal{Portal: &database.Portal{MXID: "!ready:example.com"}}
	attempts := 0
	portal, err := waitForUnblockBackfillPortal(
		context.Background(),
		100*time.Millisecond,
		time.Millisecond,
		nil,
		func(context.Context) (*bridgev2.Portal, error) {
			attempts++
			if attempts < 3 {
				return nil, nil
			}
			return readyPortal, nil
		},
	)
	if err != nil {
		t.Fatalf("waitForUnblockBackfillPortal returned error: %v", err)
	}
	if portal != readyPortal {
		t.Fatalf("waitForUnblockBackfillPortal returned portal %p, want %p", portal, readyPortal)
	}
	if attempts != 3 {
		t.Fatalf("portal lookup attempts = %d, want 3", attempts)
	}
}

func TestWaitForUnblockBackfillPortalStopsForFrameworkBackfill(t *testing.T) {
	state := newUnblockBackfillState()
	if !state.claimFramework() {
		t.Fatal("framework did not claim unblock backfill")
	}
	attempts := 0
	_, err := waitForUnblockBackfillPortal(
		context.Background(),
		100*time.Millisecond,
		time.Millisecond,
		state.frameworkStarted,
		func(context.Context) (*bridgev2.Portal, error) {
			attempts++
			return nil, nil
		},
	)
	if !errors.Is(err, errUnblockBackfillHandledByFramework) {
		t.Fatalf("waitForUnblockBackfillPortal error = %v, want framework sentinel", err)
	}
	if attempts != 0 {
		t.Fatalf("portal lookup ran %d times after framework claimed backfill", attempts)
	}
}

func TestManualUnblockBackfillClaimSuppressesFrameworkClaim(t *testing.T) {
	state := newUnblockBackfillState()
	if !state.claimManual() {
		t.Fatal("manual fallback did not claim unblock backfill")
	}
	if state.claimFramework() {
		t.Fatal("framework claimed unblock backfill after manual fallback")
	}

	state = newUnblockBackfillState()
	if !state.claimFramework() {
		t.Fatal("framework did not claim unblock backfill")
	}
	if state.claimManual() {
		t.Fatal("manual fallback claimed unblock backfill after framework")
	}
}

func TestBeginUnblockBackfillDeduplicatesChat(t *testing.T) {
	lc := &LineClient{}
	first, queued := lc.beginUnblockBackfill("U-contact")
	if !queued {
		t.Fatal("first unblock backfill was not queued")
	}
	second, queued := lc.beginUnblockBackfill("U-contact")
	if queued {
		t.Fatal("duplicate unblock backfill was queued")
	}
	if second != first {
		t.Fatal("duplicate unblock backfill returned a different state")
	}
	lc.finishUnblockBackfill("U-contact", first)
	if state := lc.getUnblockBackfill("U-contact"); state != nil {
		t.Fatal("finished unblock backfill state was not removed")
	}
}

func TestReceiveAuthProbeContextExpiresOnSchedule(t *testing.T) {
	oldInterval := receiveAuthProbeInterval
	oldNow := receiveAuthProbeNow
	oldNewContext := newReceiveAuthProbeContext
	t.Cleanup(func() {
		receiveAuthProbeInterval = oldInterval
		receiveAuthProbeNow = oldNow
		newReceiveAuthProbeContext = oldNewContext
	})

	receiveAuthProbeInterval = 10 * time.Millisecond
	receiveAuthProbeNow = time.Now
	newReceiveAuthProbeContext = func(parent context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
		return context.WithDeadlineCause(parent, deadline, errReceiveAuthProbeDue)
	}
	receiveCtx, cancel, _ := startReceiveAuthProbeContext(context.Background(), receiveAuthProbeNow())
	defer cancel()

	select {
	case <-receiveCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("receive auth probe context did not reach its deadline")
	}
	if !errors.Is(context.Cause(receiveCtx), errReceiveAuthProbeDue) {
		t.Fatalf("context cause = %v, want errReceiveAuthProbeDue", context.Cause(receiveCtx))
	}
}

func TestPollLoopRebuildsSSEClientAfterReconnect(t *testing.T) {
	oldGetLastOpRevision := getLastOpRevisionWithClient
	oldListenSSE := listenSSEWithClient
	oldReconnectDelay := sseReconnectDelay
	t.Cleanup(func() {
		getLastOpRevisionWithClient = oldGetLastOpRevision
		listenSSEWithClient = oldListenSSE
		sseReconnectDelay = oldReconnectDelay
	})

	getLastOpRevisionWithClient = func(context.Context, *line.Client) (int64, error) {
		return 1234, nil
	}
	sseReconnectDelay = time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	lc := &LineClient{
		AccessToken: "old",
		UserLogin: &bridgev2.UserLogin{
			Bridge: &bridgev2.Bridge{Log: zerolog.New(io.Discard)},
		},
	}

	var tokens []string
	listenSSEWithClient = func(client *line.Client, ctx context.Context, localRev int64, handler func(eventType, data string)) error {
		if localRev != 1234 {
			t.Fatalf("localRev = %d, want 1234", localRev)
		}
		tokens = append(tokens, client.AccessToken)
		if len(tokens) == 1 {
			lc.setTokens("new", "")
			return io.EOF
		}
		cancel()
		return context.Canceled
	}

	lc.wg.Add(1)
	lc.pollLoop(ctx)

	if len(tokens) != 2 {
		t.Fatalf("SSE attempts = %d, want 2", len(tokens))
	}
	if tokens[0] != "old" || tokens[1] != "new" {
		t.Fatalf("SSE tokens = %v, want [old new]", tokens)
	}
}

func TestPollLoopMarksLoggedOutWhenReceiveAuthFails(t *testing.T) {
	oldGetLastOpRevision := getLastOpRevisionWithClient
	oldListenSSE := listenSSEWithClient
	oldGetProfile := getProfileWithToken
	t.Cleanup(func() {
		getLastOpRevisionWithClient = oldGetLastOpRevision
		listenSSEWithClient = oldListenSSE
		getProfileWithToken = oldGetProfile
	})

	getLastOpRevisionWithClient = func(context.Context, *line.Client) (int64, error) {
		return 1234, nil
	}

	var profileCalls int
	getProfileWithToken = func(_ context.Context, token string) (*line.Profile, error) {
		profileCalls++
		if token != "stale" {
			t.Fatalf("profile token = %q, want stale", token)
		}
		return nil, errLoggedOut
	}

	listenSSEWithClient = func(client *line.Client, ctx context.Context, localRev int64, handler func(eventType, data string)) error {
		if client.AccessToken != "stale" {
			t.Fatalf("SSE client token = %q, want stale", client.AccessToken)
		}
		return errors.New("SSE error: 401")
	}

	lc := &LineClient{
		AccessToken: "stale",
		UserLogin: &bridgev2.UserLogin{
			Bridge: &bridgev2.Bridge{Log: zerolog.New(io.Discard)},
		},
	}

	lc.wg.Add(1)
	lc.pollLoop(context.Background())

	if profileCalls != 1 {
		t.Fatalf("profile calls = %d, want 1", profileCalls)
	}
	if lc.hasAccessToken() {
		t.Fatal("access token was not invalidated after receive auth logout")
	}
	if !lc.isSessionInvalidated() {
		t.Fatal("session was not marked invalidated after receive auth logout")
	}
}

func TestPollLoopMarksLoggedOutWhenReceiveIdleProbeFailsLoggedOut(t *testing.T) {
	oldGetLastOpRevision := getLastOpRevisionWithClient
	oldListenSSE := listenSSEWithClient
	t.Cleanup(func() {
		getLastOpRevisionWithClient = oldGetLastOpRevision
		listenSSEWithClient = oldListenSSE
	})

	var revisionCalls int
	getLastOpRevisionWithClient = func(_ context.Context, client *line.Client) (int64, error) {
		if client.AccessToken != "stale" {
			t.Fatalf("revision probe token = %q, want stale", client.AccessToken)
		}
		revisionCalls++
		if revisionCalls == 1 {
			return 1234, nil
		}
		return 0, errLoggedOut
	}

	listenSSEWithClient = func(client *line.Client, ctx context.Context, localRev int64, handler func(eventType, data string)) error {
		if client.AccessToken != "stale" {
			t.Fatalf("SSE client token = %q, want stale", client.AccessToken)
		}
		if localRev != 1234 {
			t.Fatalf("localRev = %d, want 1234", localRev)
		}
		return line.ErrSSEIdleTimeout
	}

	lc := &LineClient{
		AccessToken: "stale",
		UserLogin: &bridgev2.UserLogin{
			Bridge: &bridgev2.Bridge{Log: zerolog.New(io.Discard)},
		},
	}

	lc.wg.Add(1)
	lc.pollLoop(context.Background())

	if revisionCalls != 2 {
		t.Fatalf("revision calls = %d, want 2", revisionCalls)
	}
	if lc.hasAccessToken() {
		t.Fatal("access token was not invalidated after receive idle logout")
	}
	if !lc.isSessionInvalidated() {
		t.Fatal("session was not marked invalidated after receive idle logout")
	}
}

func TestPollLoopReconnectsWhenReceiveIdleProbeSucceeds(t *testing.T) {
	oldGetLastOpRevision := getLastOpRevisionWithClient
	oldListenSSE := listenSSEWithClient
	oldReconnectDelay := sseReconnectDelay
	t.Cleanup(func() {
		getLastOpRevisionWithClient = oldGetLastOpRevision
		listenSSEWithClient = oldListenSSE
		sseReconnectDelay = oldReconnectDelay
	})

	var revisionCalls int
	getLastOpRevisionWithClient = func(_ context.Context, client *line.Client) (int64, error) {
		if client.AccessToken != "valid" {
			t.Fatalf("revision probe token = %q, want valid", client.AccessToken)
		}
		revisionCalls++
		if revisionCalls == 1 {
			return 1234, nil
		}
		// The health probe sees a newer server revision, but reconnecting from it
		// would skip operations that arrived while the SSE stream was stalled.
		return 5678, nil
	}
	sseReconnectDelay = time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var listenCalls int
	listenSSEWithClient = func(client *line.Client, ctx context.Context, localRev int64, handler func(eventType, data string)) error {
		if client.AccessToken != "valid" {
			t.Fatalf("SSE client token = %q, want valid", client.AccessToken)
		}
		if localRev != 1234 {
			t.Fatalf("localRev = %d, want 1234", localRev)
		}
		listenCalls++
		if listenCalls == 1 {
			return line.ErrSSEIdleTimeout
		}
		cancel()
		return context.Canceled
	}

	lc := &LineClient{
		AccessToken: "valid",
		UserLogin: &bridgev2.UserLogin{
			Bridge: &bridgev2.Bridge{Log: zerolog.New(io.Discard)},
		},
	}

	lc.wg.Add(1)
	lc.pollLoop(ctx)

	if revisionCalls != 2 {
		t.Fatalf("revision calls = %d, want 2", revisionCalls)
	}
	if listenCalls != 2 {
		t.Fatalf("SSE attempts = %d, want 2", listenCalls)
	}
	if !lc.hasAccessToken() {
		t.Fatal("valid access token was invalidated")
	}
	if lc.isSessionInvalidated() {
		t.Fatal("valid session was marked invalidated")
	}
}

func TestPollLoopReceiveAuthDeadlineIgnoresHeartbeats(t *testing.T) {
	deadlineCancels := installManualReceiveAuthProbeDeadline(t)
	oldGetLastOpRevision := getLastOpRevisionWithClient
	oldListenSSE := listenSSEWithClient
	t.Cleanup(func() {
		getLastOpRevisionWithClient = oldGetLastOpRevision
		listenSSEWithClient = oldListenSSE
	})

	var revisionCalls int
	getLastOpRevisionWithClient = func(_ context.Context, client *line.Client) (int64, error) {
		if client.AccessToken != "stale" {
			t.Fatalf("revision probe token = %q, want stale", client.AccessToken)
		}
		revisionCalls++
		if revisionCalls == 1 {
			return 1234, nil
		}
		return 0, errLoggedOut
	}

	var heartbeatCalls int
	listenSSEWithClient = func(_ *line.Client, ctx context.Context, localRev int64, handler func(eventType, data string)) error {
		if localRev != 1234 {
			t.Fatalf("localRev = %d, want 1234", localRev)
		}
		for range 5 {
			heartbeatCalls++
			handler("ping", "null")
			handler("connInfoRevision", "42")
		}
		(<-deadlineCancels)(errReceiveAuthProbeDue)
		<-ctx.Done()
		return ctx.Err()
	}

	lc := &LineClient{
		AccessToken: "stale",
		UserLogin: &bridgev2.UserLogin{
			Bridge: &bridgev2.Bridge{Log: zerolog.New(io.Discard)},
		},
	}
	lc.wg.Add(1)
	lc.pollLoop(context.Background())

	if heartbeatCalls != 5 {
		t.Fatalf("heartbeat batches = %d, want 5", heartbeatCalls)
	}
	if revisionCalls != 2 {
		t.Fatalf("revision calls = %d, want startup plus one deadline probe", revisionCalls)
	}
	if lc.hasAccessToken() {
		t.Fatal("access token was not invalidated after deadline probe")
	}
	if !lc.isSessionInvalidated() {
		t.Fatal("session was not marked invalidated after deadline probe")
	}
}

func TestPollLoopReceiveAuthDeadlineSurvivesEOFReconnects(t *testing.T) {
	deadlineCancels := installManualReceiveAuthProbeDeadline(t)
	oldGetLastOpRevision := getLastOpRevisionWithClient
	oldListenSSE := listenSSEWithClient
	oldReconnectDelay := sseReconnectDelay
	t.Cleanup(func() {
		getLastOpRevisionWithClient = oldGetLastOpRevision
		listenSSEWithClient = oldListenSSE
		sseReconnectDelay = oldReconnectDelay
	})
	sseReconnectDelay = 0

	var revisionCalls int
	getLastOpRevisionWithClient = func(context.Context, *line.Client) (int64, error) {
		revisionCalls++
		if revisionCalls == 1 {
			return 1234, nil
		}
		return 0, errLoggedOut
	}

	var listenCalls int
	listenSSEWithClient = func(_ *line.Client, _ context.Context, localRev int64, _ func(eventType, data string)) error {
		if localRev != 1234 {
			t.Fatalf("localRev = %d, want 1234", localRev)
		}
		listenCalls++
		if listenCalls == 3 {
			(<-deadlineCancels)(errReceiveAuthProbeDue)
		}
		return io.EOF
	}

	lc := &LineClient{
		AccessToken: "stale",
		UserLogin: &bridgev2.UserLogin{
			Bridge: &bridgev2.Bridge{Log: zerolog.New(io.Discard)},
		},
	}
	lc.wg.Add(1)
	lc.pollLoop(context.Background())

	if listenCalls != 3 {
		t.Fatalf("SSE attempts = %d, want 3", listenCalls)
	}
	if revisionCalls != 2 {
		t.Fatalf("revision calls = %d, want startup plus one deadline probe", revisionCalls)
	}
}

func TestPollLoopSuccessfulReceiveAuthProbePreservesLocalRev(t *testing.T) {
	deadlineCancels := installManualReceiveAuthProbeDeadline(t)
	oldGetLastOpRevision := getLastOpRevisionWithClient
	oldListenSSE := listenSSEWithClient
	t.Cleanup(func() {
		getLastOpRevisionWithClient = oldGetLastOpRevision
		listenSSEWithClient = oldListenSSE
	})

	var revisionCalls int
	getLastOpRevisionWithClient = func(context.Context, *line.Client) (int64, error) {
		revisionCalls++
		if revisionCalls == 1 {
			return 1234, nil
		}
		return 5678, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var listenCalls int
	listenSSEWithClient = func(_ *line.Client, receiveCtx context.Context, localRev int64, _ func(eventType, data string)) error {
		if localRev != 1234 {
			t.Fatalf("localRev = %d, want health probe result to remain ignored", localRev)
		}
		listenCalls++
		if listenCalls == 1 {
			(<-deadlineCancels)(errReceiveAuthProbeDue)
			<-receiveCtx.Done()
			return receiveCtx.Err()
		}
		cancel()
		<-receiveCtx.Done()
		return receiveCtx.Err()
	}

	lc := &LineClient{
		AccessToken: "valid",
		UserLogin: &bridgev2.UserLogin{
			Bridge: &bridgev2.Bridge{Log: zerolog.New(io.Discard)},
		},
	}
	lc.wg.Add(1)
	lc.pollLoop(ctx)

	if revisionCalls != 2 {
		t.Fatalf("revision calls = %d, want startup plus one successful probe", revisionCalls)
	}
	if listenCalls != 2 {
		t.Fatalf("SSE attempts = %d, want reconnect after deadline probe", listenCalls)
	}
	if !lc.hasAccessToken() || lc.isSessionInvalidated() {
		t.Fatal("successful health probe changed valid session state")
	}
}

func TestPollLoopParentCancellationDoesNotProbeAuth(t *testing.T) {
	installManualReceiveAuthProbeDeadline(t)
	oldGetLastOpRevision := getLastOpRevisionWithClient
	oldListenSSE := listenSSEWithClient
	t.Cleanup(func() {
		getLastOpRevisionWithClient = oldGetLastOpRevision
		listenSSEWithClient = oldListenSSE
	})

	var revisionCalls int
	getLastOpRevisionWithClient = func(context.Context, *line.Client) (int64, error) {
		revisionCalls++
		return 1234, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	listenSSEWithClient = func(_ *line.Client, receiveCtx context.Context, _ int64, _ func(eventType, data string)) error {
		cancel()
		<-receiveCtx.Done()
		return receiveCtx.Err()
	}

	lc := &LineClient{
		AccessToken: "valid",
		UserLogin: &bridgev2.UserLogin{
			Bridge: &bridgev2.Bridge{Log: zerolog.New(io.Discard)},
		},
	}
	lc.wg.Add(1)
	lc.pollLoop(ctx)

	if revisionCalls != 1 {
		t.Fatalf("revision calls = %d, want startup only", revisionCalls)
	}
	if !lc.hasAccessToken() || lc.isSessionInvalidated() {
		t.Fatal("parent cancellation changed session state")
	}
}

func TestReceiveRequestNeedLoginMarksLoggedOutImmediately(t *testing.T) {
	oldGetProfile := getProfileWithToken
	t.Cleanup(func() {
		getProfileWithToken = oldGetProfile
	})

	getProfileWithToken = func(_ context.Context, token string) (*line.Profile, error) {
		t.Fatal("REQUEST_NEED_LOGIN should be handled without probing profile")
		return nil, nil
	}

	lc := &LineClient{AccessToken: "stale"}
	stopped := lc.handleReceiveAuthError(context.Background(), errors.New(`SSE error: 401: {"code":10004,"message":"REQUEST_NEED_LOGIN"}`))

	if !stopped {
		t.Fatal("receive auth handler should stop on REQUEST_NEED_LOGIN")
	}
	if lc.hasAccessToken() {
		t.Fatal("access token was not invalidated")
	}
	if !lc.isSessionInvalidated() {
		t.Fatal("session was not marked invalidated")
	}
}

func TestReceiveAuthErrorWithValidProfileDoesNotRecover(t *testing.T) {
	oldGetProfile := getProfileWithToken
	t.Cleanup(func() {
		getProfileWithToken = oldGetProfile
	})

	var profileCalls int
	getProfileWithToken = func(_ context.Context, token string) (*line.Profile, error) {
		profileCalls++
		if token != "valid" {
			t.Fatalf("profile token = %q, want valid", token)
		}
		return &line.Profile{}, nil
	}

	lc := &LineClient{AccessToken: "valid"}
	stopped := lc.handleReceiveAuthError(context.Background(), errors.New("SSE error: 401"))

	if stopped {
		t.Fatal("receive auth handler should reconnect without stopping when the profile probe succeeds")
	}
	if profileCalls != 1 {
		t.Fatalf("profile calls = %d, want 1", profileCalls)
	}
	if !lc.hasAccessToken() {
		t.Fatal("valid access token was invalidated")
	}
	if lc.isSessionInvalidated() {
		t.Fatal("valid session was marked invalidated")
	}
}

func TestReceiveAuthErrorCancellationDuringProfileDoesNotInvalidate(t *testing.T) {
	oldGetProfile := getProfileWithToken
	t.Cleanup(func() {
		getProfileWithToken = oldGetProfile
	})

	profileStarted := make(chan struct{})
	getProfileWithToken = func(ctx context.Context, token string) (*line.Profile, error) {
		if token != "valid" {
			t.Fatalf("profile token = %q, want valid", token)
		}
		close(profileStarted)
		<-ctx.Done()
		return nil, errLoggedOut
	}

	ctx, cancel := context.WithCancel(context.Background())
	lc := &LineClient{AccessToken: "valid"}
	result := make(chan bool, 1)
	go func() {
		result <- lc.handleReceiveAuthError(ctx, errors.New("SSE error: 401"))
	}()
	<-profileStarted
	cancel()

	select {
	case stopped := <-result:
		if !stopped {
			t.Fatal("canceled receive auth handler should stop")
		}
	case <-time.After(time.Second):
		t.Fatal("receive auth handler did not stop after cancellation")
	}
	if !lc.hasAccessToken() || lc.isSessionInvalidated() {
		t.Fatal("ordinary cancellation was misclassified as forced logout")
	}
}
