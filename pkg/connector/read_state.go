package connector

import (
	"context"
	"slices"
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"

	"github.com/beeper/line/pkg/line"
)

func (lc *LineClient) chatsMissingLatestMessage(ctx context.Context, messageBoxes []line.MessageBox, chatMIDs []string) []string {
	var missing []string
	for _, box := range messageBoxes {
		if box.LastDeliveredMessageID == nil || box.LastDeliveredMessageID.MessageID == "" || !slices.Contains(chatMIDs, box.ID) {
			continue
		}
		msg, err := lc.UserLogin.Bridge.DB.Message.GetFirstPartByID(ctx, lc.UserLogin.ID, networkid.MessageID(box.LastDeliveredMessageID.MessageID))
		if err != nil {
			lc.UserLogin.Bridge.Log.Warn().Err(err).Str("chat_mid", box.ID).Msg("Failed to check latest bridged message before catch-up")
			continue
		}
		if msg == nil {
			missing = append(missing, box.ID)
		}
	}
	return missing
}

func (lc *LineClient) queueStartupReadReceipts(ctx context.Context, chatMIDs []string) {
	for chunk := range slices.Chunk(chatMIDs, messageBoxPageLimit) {
		_, boxes, err := callLineResult(lc, ctx, func(client *line.Client) (map[string]line.MessageBox, error) {
			return client.GetMessageBoxesByIDsContext(ctx, chunk)
		})
		if err != nil {
			lc.UserLogin.Bridge.Log.Warn().Err(err).Int("chat_count", len(chunk)).Msg("Failed to fetch read state after catch-up")
			continue
		}
		now := time.Now()
		for _, mid := range chunk {
			lastSeen := boxes[mid].LastSeenMessageID
			if lastSeen == "" || lastSeen == "0" || strings.HasPrefix(lastSeen, "-") {
				continue
			}
			lc.UserLogin.Bridge.QueueRemoteEvent(lc.UserLogin, &simplevent.Receipt{
				EventMeta: simplevent.EventMeta{
					Type:      bridgev2.RemoteEventReadReceipt,
					PortalKey: networkid.PortalKey{ID: makePortalID(mid), Receiver: lc.UserLogin.ID},
					Sender:    lc.eventSenderForMID(lc.Mid),
					Timestamp: now,
				},
				LastTarget: networkid.MessageID(lastSeen),
			})
		}
	}
}
