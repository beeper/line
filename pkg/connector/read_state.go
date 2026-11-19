package connector

import (
	"context"
	"database/sql"
	"errors"
	"math/big"
	"slices"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"

	"github.com/beeper/line/pkg/line"
)

func (lc *LineClient) ownReadTarget(mid string) string {
	lc.cacheMu.Lock()
	defer lc.cacheMu.Unlock()
	return lc.ownReadTargets[mid]
}

func (lc *LineClient) noteOwnRead(mid, target string) {
	readID, ok := new(big.Int).SetString(target, 10)
	if !ok || readID.Sign() <= 0 {
		return
	}
	lc.cacheMu.Lock()
	defer lc.cacheMu.Unlock()
	if previous, valid := new(big.Int).SetString(lc.ownReadTargets[mid], 10); valid && previous.Cmp(readID) >= 0 {
		return
	}
	if lc.ownReadTargets == nil {
		lc.ownReadTargets = make(map[string]string)
	}
	lc.ownReadTargets[mid] = readID.String()
}

func (lc *LineClient) queueStartupReadReceipts(ctx context.Context, caughtUp map[string][]networkid.MessageID) {
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
			lc.UserLogin.Bridge.QueueRemoteEvent(lc.UserLogin, lc.startupReadReceipt(mid, boxes[mid].LastSeenMessageID, caughtUp[mid]))
		}
	}
}

func (lc *LineClient) startupReadReceipt(mid, lastSeen string, caughtUp []networkid.MessageID) *simplevent.Receipt {
	receipt := &simplevent.Receipt{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventReadReceipt,
			PortalKey: networkid.PortalKey{ID: makePortalID(mid), Receiver: lc.UserLogin.ID},
			Sender:    lc.eventSenderForMID(lc.Mid),
		},
	}
	receipt.PreHandleWithResultFunc = func(ctx context.Context, portal *bridgev2.Portal) *bridgev2.EventHandlingResult {
		ignore := bridgev2.EventHandlingResultIgnored
		fail := func(err error) *bridgev2.EventHandlingResult {
			result := bridgev2.EventHandlingResultFailed.WithError(err)
			return &result
		}
		readID, valid := new(big.Int).SetString(lastSeen, 10)
		if !valid || readID.Sign() <= 0 {
			readID = new(big.Int)
		}
		if ownReadID, ok := new(big.Int).SetString(lc.ownReadTarget(mid), 10); ok && ownReadID.Cmp(readID) > 0 {
			readID = ownReadID
		}
		if readID.Sign() <= 0 {
			return &ignore
		}
		hasReadCatchUp := false
		for _, msgID := range caughtUp {
			candidateID, ok := new(big.Int).SetString(string(msgID), 10)
			if !ok || candidateID.Sign() <= 0 || candidateID.Cmp(readID) > 0 {
				continue
			}
			msg, err := lc.UserLogin.Bridge.DB.Message.GetLastPartByID(ctx, lc.UserLogin.ID, msgID)
			if err != nil {
				return fail(err)
			}
			if msg != nil && msg.Room == portal.PortalKey && !msg.HasFakeMXID() {
				hasReadCatchUp = true
				break
			}
		}
		if !hasReadCatchUp {
			return &ignore
		}
		target, err := lc.UserLogin.Bridge.DB.Message.GetLastPartByID(ctx, lc.UserLogin.ID, networkid.MessageID(readID.String()))
		if err != nil {
			return fail(err)
		}
		if target == nil || target.Room != portal.PortalKey || target.HasFakeMXID() {
			var rowID int64
			err = lc.UserLogin.Bridge.DB.QueryRow(ctx, `
				SELECT rowid FROM message
				WHERE bridge_id=$1 AND room_id=$2 AND room_receiver=$3
				  AND ltrim(id, '0123456789')='' AND ltrim(id, '0')<>''
				  AND (length(ltrim(id, '0'))<length($4)
				       OR (length(ltrim(id, '0'))=length($4) AND ltrim(id, '0')<=$4))
				  AND mxid NOT LIKE '~fake:%'
				ORDER BY length(ltrim(id, '0')) DESC, ltrim(id, '0') DESC, part_id DESC LIMIT 1
			`, lc.UserLogin.Bridge.ID, portal.ID, portal.Receiver, readID.String()).Scan(&rowID)
			if errors.Is(err, sql.ErrNoRows) {
				return &ignore
			} else if err != nil {
				return fail(err)
			}
			target, err = lc.UserLogin.Bridge.DB.Message.GetByRowID(ctx, rowID)
			if err != nil {
				return fail(err)
			} else if target == nil {
				return &ignore
			}
		}
		receipt.LastTarget = target.ID
		receipt.Timestamp = target.Timestamp
		return nil
	}
	return receipt
}
