package connector

import (
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"

	"github.com/beeper/line/pkg/line"
)

func (lc *LineClient) queueStartupReadReceipts(messageBoxes []line.MessageBox, chatMIDs []string) {
	prefetched := make(map[string]struct{}, len(chatMIDs))
	for _, mid := range chatMIDs {
		prefetched[mid] = struct{}{}
	}
	now := time.Now()
	for _, box := range messageBoxes {
		lastSeen := box.LastSeenMessageID
		if _, ok := prefetched[box.ID]; !ok || lastSeen == "" || lastSeen == "0" || strings.HasPrefix(lastSeen, "-") {
			continue
		}
		lc.UserLogin.Bridge.QueueRemoteEvent(lc.UserLogin, &simplevent.Receipt{
			EventMeta: simplevent.EventMeta{
				Type:      bridgev2.RemoteEventReadReceipt,
				PortalKey: networkid.PortalKey{ID: makePortalID(box.ID), Receiver: lc.UserLogin.ID},
				Sender:    lc.eventSenderForMID(lc.Mid),
				Timestamp: now,
			},
			LastTarget: networkid.MessageID(lastSeen),
		})
	}
}
