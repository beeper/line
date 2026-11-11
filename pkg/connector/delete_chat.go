package connector

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"

	"github.com/beeper/line/pkg/line"
)

var _ bridgev2.DeleteChatHandlingNetworkAPI = (*LineClient)(nil)

func (lc *LineClient) HandleMatrixDeleteChat(ctx context.Context, msg *bridgev2.MatrixDeleteChat) error {
	if msg.Content.DeleteForEveryone {
		return errors.New("delete for everyone is not supported on LINE")
	}
	chatMid := string(msg.Portal.ID)
	if msg.Portal.MessageRequest {
		reqSeq := lc.nextUntrackedReqSeq()
		_, err := lc.callLine(ctx, func(client *line.Client) error {
			return client.RejectChatInvitationContext(ctx, int64(reqSeq), chatMid)
		})
		return wrapSendError(err)
	}
	_, boxes, err := callLineResult(lc, ctx, func(client *line.Client) (map[string]line.MessageBox, error) {
		return client.GetMessageBoxesByIDsContext(ctx, []string{chatMid})
	})
	if err != nil {
		return wrapSendError(err)
	}
	lastDelivered := boxes[chatMid].LastDeliveredMessageID
	if lastDelivered == nil || lastDelivered.MessageID == "" || lastDelivered.MessageID == "-1" {
		return lc.saveChatDeletion(ctx, chatMid, "0")
	}
	if cutoff, ok := new(big.Int).SetString(lastDelivered.MessageID, 10); !ok || cutoff.Sign() < 0 {
		return errors.New("invalid last delivered message ID")
	}
	reqSeq := lc.nextReqSeq()
	_, err = lc.callLine(ctx, func(client *line.Client) error {
		return client.SendChatRemovedContext(ctx, int64(reqSeq), chatMid, lastDelivered.MessageID, 0)
	})
	if err != nil {
		return wrapSendError(err)
	}
	return lc.saveChatDeletion(context.WithoutCancel(ctx), chatMid, lastDelivered.MessageID)
}

func (lc *LineClient) handleChatRemovedElsewhere(ctx context.Context, op line.Operation) {
	chatMid := op.Param1
	if chatMid == "" {
		return
	}
	ts := time.Now()
	if createdAt, err := op.CreatedTime.Int64(); err == nil && createdAt > 0 {
		ts = time.UnixMilli(createdAt)
	}
	portalKey := networkid.PortalKey{ID: makePortalID(chatMid), Receiver: lc.UserLogin.ID}
	log := lc.UserLogin.Log.With().Str("chat_mid", chatMid).Str("last_message_id", op.Param2).Logger()
	cutoff := op.Param2
	if id, ok := new(big.Int).SetString(cutoff, 10); !ok || id.Sign() < 0 {
		cutoff = "0"
		lastMsg, err := lc.UserLogin.Bridge.DB.Message.GetLastPartAtOrBeforeTime(ctx, portalKey, ts)
		if err != nil {
			log.Err(err).Msg("Failed to get last bridged message for chat removed on another device")
			return
		} else if lastMsg != nil {
			if id, ok := new(big.Int).SetString(string(lastMsg.ID), 10); ok && id.Sign() >= 0 {
				cutoff = string(lastMsg.ID)
			}
		}
	}
	if err := lc.saveChatDeletion(ctx, chatMid, cutoff); err != nil {
		log.Err(err).Msg("Failed to save deletion of chat removed on another device")
		return
	}
	log.Info().Str("cutoff", cutoff).Msg("Chat removed on another device")
	lc.UserLogin.Bridge.QueueRemoteEvent(lc.UserLogin, &simplevent.ChatDelete{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventChatDelete,
			PortalKey: portalKey,
			Sender:    lc.eventSenderForMID(string(lc.UserLogin.ID)),
			Timestamp: ts,
		},
		OnlyForMe: true,
	})
}

func (lc *LineClient) chatDeletionPrefix() string {
	return fmt.Sprintf("line_deleted_chat:%+q:", lc.UserLogin.ID)
}

func (lc *LineClient) loadChatDeletions(ctx context.Context) error {
	prefix := lc.chatDeletionPrefix()
	rows, err := lc.UserLogin.Bridge.DB.KV.Query(ctx,
		`SELECT key, value FROM kv_store WHERE bridge_id = $1 AND substr(key, 1, $2) = $3`,
		lc.UserLogin.Bridge.ID, len(prefix), prefix)
	if err != nil {
		return err
	}
	defer rows.Close()
	lc.deletedChats = make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return err
		}
		if cutoff, ok := new(big.Int).SetString(value, 10); !ok || cutoff.Sign() < 0 {
			return errors.New("invalid stored chat deletion cutoff")
		}
		lc.deletedChats[key[len(prefix):]] = value
	}
	return rows.Err()
}

func (lc *LineClient) saveChatDeletion(ctx context.Context, mid, cutoff string) error {
	lc.deletedChatsMu.Lock()
	defer lc.deletedChatsMu.Unlock()
	if previous, ok := lc.deletedChats[mid]; ok {
		oldID, _ := new(big.Int).SetString(previous, 10)
		newID, _ := new(big.Int).SetString(cutoff, 10)
		if oldID.Cmp(newID) > 0 {
			cutoff = previous
		}
	}
	_, err := lc.UserLogin.Bridge.DB.KV.Exec(ctx,
		`INSERT INTO kv_store (bridge_id, key, value) VALUES ($1, $2, $3)
		 ON CONFLICT (bridge_id, key) DO UPDATE SET value = $3`,
		lc.UserLogin.Bridge.ID, lc.chatDeletionPrefix()+mid, cutoff)
	if err != nil {
		return err
	}
	if lc.deletedChats == nil {
		lc.deletedChats = make(map[string]string)
	}
	lc.deletedChats[mid] = cutoff
	return nil
}

func (lc *LineClient) shouldSkipDeletedChat(mid, messageID string) bool {
	lc.deletedChatsMu.Lock()
	cutoff, deleted := lc.deletedChats[mid]
	lc.deletedChatsMu.Unlock()
	if !deleted {
		return false
	}
	oldID, _ := new(big.Int).SetString(cutoff, 10)
	newID, ok := new(big.Int).SetString(messageID, 10)
	// Retain the cutoff after reopening so backfill cannot restore deleted history.
	return !ok || newID.Cmp(oldID) <= 0
}
