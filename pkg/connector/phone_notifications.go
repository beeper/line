package connector

import (
	"context"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

var updateSettingsAttributes2WithClient = func(
	ctx context.Context,
	client *line.Client,
	reqSeq int64,
	attributes []int,
	settings line.Settings,
) error {
	return client.UpdateSettingsAttributes2Context(ctx, reqSeq, attributes, settings)
}

func (lc *LineClient) preservePhoneNotifications(ctx context.Context) error {
	reqSeq := int64(lc.nextUntrackedReqSeq())
	notificationDisabledWithSub := false
	_, err := lc.callLine(ctx, func(client *line.Client) error {
		return updateSettingsAttributes2WithClient(
			ctx,
			client,
			reqSeq,
			[]int{line.SettingsAttributeNotificationDisabledWithSub},
			line.Settings{NotificationDisabledWithSub: &notificationDisabledWithSub},
		)
	})
	return err
}

// configurePhoneNotifications is best-effort because updating this optional
// preference must not prevent an otherwise healthy bridge session from connecting.
func (lc *LineClient) configurePhoneNotifications(ctx context.Context) {
	err := lc.preservePhoneNotifications(ctx)
	if err == nil || ctx.Err() != nil || lc.isSessionInvalidated() {
		return
	}
	lc.UserLogin.Bridge.Log.Warn().Err(err).
		Msg("Failed to preserve LINE phone notifications")
}
