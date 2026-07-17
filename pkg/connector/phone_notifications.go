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
	reqSeq := int64(lc.nextReqSeq())
	_, err := lc.callLine(ctx, func(client *line.Client) error {
		return updateSettingsAttributes2WithClient(
			ctx,
			client,
			reqSeq,
			[]int{line.SettingsAttributeNotificationDisabledWithSub},
			line.Settings{NotificationDisabledWithSub: false},
		)
	})
	return err
}

// configurePhoneNotifications is best-effort for ordinary settings failures.
// Auth failures are returned because startup must not announce a stale session
// as connected when refresh and re-login could not recover it.
func (lc *LineClient) configurePhoneNotifications(ctx context.Context) error {
	err := lc.preservePhoneNotifications(ctx)
	if err == nil || ctx.Err() != nil || line.IsAuthError(err) {
		return err
	}
	lc.UserLogin.Bridge.Log.Warn().Err(err).
		Msg("Failed to preserve LINE phone notifications")
	return nil
}
