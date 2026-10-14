package connector

import (
	"context"
	"errors"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"

	"github.com/beeper/line/pkg/line"
)

var _ bridgev2.UserBlockingNetworkAPI = (*LineClient)(nil)

func (lc *LineClient) HandleMatrixBlockUser(ctx context.Context, msg *bridgev2.MatrixBlockUser) error {
	if msg.Content.ReportSpam {
		return errors.New("spam reporting is not supported")
	}
	mid := string(msg.Portal.ID)
	if mid == "" || isChatMID(mid) || lc.isOwnMID(mid) {
		return errors.New("DM recipient is unknown")
	}
	reqSeq := lc.nextUntrackedReqSeq()
	_, err := lc.callLine(ctx, func(client *line.Client) error {
		return client.SetContactBlocked(ctx, int64(reqSeq), mid, msg.Content.Block)
	})
	return err
}

func (lc *LineClient) queueUserBlocked(ctx context.Context, mid string, blocked bool) bool {
	portalKey := networkid.PortalKey{ID: makePortalID(mid), Receiver: lc.UserLogin.ID}
	portal, err := lc.UserLogin.Bridge.GetExistingPortalByKey(ctx, portalKey)
	if err != nil || portal == nil || portal.MXID == "" {
		return false
	}
	lc.UserLogin.QueueRemoteEvent(&simplevent.ChatInfoChange{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventChatInfoChange,
			PortalKey: portalKey,
			Timestamp: time.Now(),
		},
		ChatInfoChange: &bridgev2.ChatInfoChange{
			ChatInfo: &bridgev2.ChatInfo{UserBlocked: &blocked},
		},
	})
	return true
}
