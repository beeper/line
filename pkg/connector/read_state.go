package connector

import (
	"context"
	"math/big"
	"slices"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"

	"github.com/beeper/line/pkg/line"
)

func (lc *LineClient) ownReadVersion(mid string) uint64 {
	lc.cacheMu.Lock()
	defer lc.cacheMu.Unlock()
	return lc.ownReadVersions[mid]
}

func (lc *LineClient) noteOwnRead(mid string) {
	lc.cacheMu.Lock()
	defer lc.cacheMu.Unlock()
	if lc.ownReadVersions == nil {
		lc.ownReadVersions = make(map[string]uint64)
	}
	lc.ownReadVersions[mid]++
}

func (lc *LineClient) queueStartupReadReceipts(ctx context.Context, caughtUp map[string][]networkid.MessageID, versions map[string]uint64) {
	chatMIDs := make([]string, 0, len(caughtUp))
	for mid := range caughtUp {
		chatMIDs = append(chatMIDs, mid)
	}
	for chunk := range slices.Chunk(chatMIDs, messageBoxPageLimit) {
		_, boxes, err := callLineResult(lc, ctx, func(client *line.Client) (map[string]line.MessageBox, error) {
			return client.GetMessageBoxesByIDsContext(ctx, chunk)
		})
		if err != nil {
			lc.UserLogin.Bridge.Log.Warn().Err(err).Int("chat_count", len(chunk)).Msg("Failed to fetch read state after catch-up")
			continue
		}
		for _, mid := range chunk {
			if receipt := lc.startupReadReceipt(mid, boxes[mid].LastSeenMessageID, caughtUp[mid], versions[mid]); receipt != nil {
				lc.UserLogin.Bridge.QueueRemoteEvent(lc.UserLogin, receipt)
			}
		}
	}
}

func (lc *LineClient) startupReadReceipt(mid, lastSeen string, caughtUp []networkid.MessageID, version uint64) *simplevent.Receipt {
	readID, ok := new(big.Int).SetString(lastSeen, 10)
	if !ok || readID.Sign() <= 0 {
		return nil
	}
	receipt := &simplevent.Receipt{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventReadReceipt,
			PortalKey: networkid.PortalKey{ID: makePortalID(mid), Receiver: lc.UserLogin.ID},
			Sender:    lc.eventSenderForMID(lc.Mid),
		},
	}
	receipt.PreHandleWithResultFunc = func(ctx context.Context, portal *bridgev2.Portal) *bridgev2.EventHandlingResult {
		var target *database.Message
		var targetID *big.Int
		for _, msgID := range caughtUp {
			candidateID, valid := new(big.Int).SetString(string(msgID), 10)
			if !valid || candidateID.Sign() <= 0 || candidateID.Cmp(readID) > 0 {
				continue
			}
			msg, err := lc.UserLogin.Bridge.DB.Message.GetLastPartByID(ctx, lc.UserLogin.ID, msgID)
			if err != nil {
				result := bridgev2.EventHandlingResultFailed.WithError(err)
				return &result
			}
			if msg != nil && msg.Room == portal.PortalKey && !msg.HasFakeMXID() && (targetID == nil || candidateID.Cmp(targetID) > 0) {
				target = msg
				targetID = candidateID
			}
		}
		if target == nil || lc.ownReadVersion(mid) != version {
			result := bridgev2.EventHandlingResultIgnored
			return &result
		}
		receipt.LastTarget = target.ID
		receipt.Timestamp = target.Timestamp
		return nil
	}
	return receipt
}
