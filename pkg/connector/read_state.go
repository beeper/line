package connector

import (
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"

	"github.com/beeper/line/pkg/line"
)

func (lc *LineClient) queueOwnReadReceipt(chatMid, lastSeenMessageID string, ts time.Time) {
	if chatMid == "" || lastSeenMessageID == "" || lastSeenMessageID == "0" || strings.HasPrefix(lastSeenMessageID, "-") {
		return
	}
	lc.UserLogin.Bridge.QueueRemoteEvent(lc.UserLogin, &simplevent.Receipt{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventReadReceipt,
			PortalKey: networkid.PortalKey{ID: makePortalID(chatMid), Receiver: lc.UserLogin.ID},
			Sender:    lc.eventSenderForMID(string(lc.UserLogin.ID)),
			Timestamp: ts,
		},
		LastTarget: networkid.MessageID(lastSeenMessageID),
	})
}

func (lc *LineClient) queueStartupReadReceipts(messageBoxes []line.MessageBox, chatMIDs []string) {
	prefetched := make(map[string]struct{}, len(chatMIDs))
	for _, mid := range chatMIDs {
		prefetched[mid] = struct{}{}
	}
	for _, box := range messageBoxes {
		if _, ok := prefetched[box.ID]; ok {
			lc.queueOwnReadReceipt(box.ID, box.LastSeenMessageID, time.Now())
		}
	}
}
