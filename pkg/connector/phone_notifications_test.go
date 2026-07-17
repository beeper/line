package connector

import (
	"context"
	"errors"
	"io"
	"slices"
	"testing"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

func TestPreservePhoneNotificationsUsesReqSeqAndAuthRecovery(t *testing.T) {
	oldUpdateSettings := updateSettingsAttributes2WithClient
	oldRecover := recoverLineToken
	t.Cleanup(func() {
		updateSettingsAttributes2WithClient = oldUpdateSettings
		recoverLineToken = oldRecover
	})

	lc := &LineClient{AccessToken: "expired-token"}
	var (
		calls      int
		recoveries int
		tokens     []string
		reqSeqs    []int64
	)
	updateSettingsAttributes2WithClient = func(
		_ context.Context,
		client *line.Client,
		reqSeq int64,
		attributes []int,
		settings line.Settings,
	) error {
		calls++
		tokens = append(tokens, client.AccessToken)
		reqSeqs = append(reqSeqs, reqSeq)
		if !slices.Equal(attributes, []int{line.SettingsAttributeNotificationDisabledWithSub}) {
			t.Fatalf("attributes = %v, want [%d]", attributes, line.SettingsAttributeNotificationDisabledWithSub)
		}
		if settings.NotificationDisabledWithSub {
			t.Fatal("notificationDisabledWithSub = true, want false")
		}
		if calls == 1 {
			return errAuthRequired
		}
		return nil
	}
	recoverLineToken = func(client *LineClient, _ context.Context) error {
		recoveries++
		client.setTokens("fresh-token", "")
		return nil
	}

	err := lc.preservePhoneNotifications(context.Background())
	if err != nil {
		t.Fatalf("preservePhoneNotifications returned error: %v", err)
	}
	if calls != 2 || recoveries != 1 {
		t.Fatalf("calls/recoveries = %d/%d, want 2/1", calls, recoveries)
	}
	if !slices.Equal(tokens, []string{"expired-token", "fresh-token"}) {
		t.Fatalf("client tokens = %v, want [expired-token fresh-token]", tokens)
	}
	if len(reqSeqs) != 2 || reqSeqs[0] <= 0 || reqSeqs[0] != reqSeqs[1] {
		t.Fatalf("request sequences = %v, want the same positive value for the retry", reqSeqs)
	}
	if !lc.consumeSentReqSeq(int(reqSeqs[0])) {
		t.Fatalf("request sequence %d was not tracked", reqSeqs[0])
	}
}

func TestConfigurePhoneNotificationsIsBestEffort(t *testing.T) {
	oldUpdateSettings := updateSettingsAttributes2WithClient
	t.Cleanup(func() {
		updateSettingsAttributes2WithClient = oldUpdateSettings
	})

	log := zerolog.New(io.Discard)
	lc := &LineClient{
		AccessToken: "healthy-token",
		UserLogin: &bridgev2.UserLogin{
			Bridge: &bridgev2.Bridge{Log: log},
		},
	}
	var calls int
	updateSettingsAttributes2WithClient = func(
		context.Context,
		*line.Client,
		int64,
		[]int,
		line.Settings,
	) error {
		calls++
		return errors.New("settings endpoint temporarily unavailable")
	}

	ctx := context.Background()
	if err := lc.configurePhoneNotifications(ctx); err != nil {
		t.Fatalf("configurePhoneNotifications returned best-effort error: %v", err)
	}

	if calls != 1 {
		t.Fatalf("settings calls = %d, want 1", calls)
	}
	if ctx.Err() != nil {
		t.Fatalf("healthy connect context was canceled: %v", ctx.Err())
	}
	if lc.getAccessToken() != "healthy-token" || lc.isSessionInvalidated() {
		t.Fatal("best-effort settings failure changed healthy login state")
	}
}

func TestConfigurePhoneNotificationsReturnsUnrecoverableAuthError(t *testing.T) {
	oldUpdateSettings := updateSettingsAttributes2WithClient
	oldRecover := recoverLineToken
	t.Cleanup(func() {
		updateSettingsAttributes2WithClient = oldUpdateSettings
		recoverLineToken = oldRecover
	})

	log := zerolog.New(io.Discard)
	lc := &LineClient{
		AccessToken: "expired-token",
		UserLogin: &bridgev2.UserLogin{
			Bridge: &bridgev2.Bridge{Log: log},
		},
	}
	updateSettingsAttributes2WithClient = func(
		context.Context,
		*line.Client,
		int64,
		[]int,
		line.Settings,
	) error {
		return errAuthRequired
	}
	recoveryErr := errors.New("stored credentials rejected")
	recoverLineToken = func(*LineClient, context.Context) error {
		return recoveryErr
	}

	err := lc.configurePhoneNotifications(context.Background())
	if err == nil {
		t.Fatal("expected unrecoverable auth error")
	}
	if !line.IsAuthError(err) || !errors.Is(err, errAuthRequired) || !errors.Is(err, recoveryErr) {
		t.Fatalf("configurePhoneNotifications error = %v, want auth and recovery details", err)
	}
}
