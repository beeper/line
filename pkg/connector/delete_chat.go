package connector

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"maunium.net/go/mautrix/bridgev2"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
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
		return err
	}
	_, boxes, err := callLineResult(lc, ctx, func(client *line.Client) (map[string]line.MessageBox, error) {
		return client.GetMessageBoxesByIDsContext(ctx, []string{chatMid})
	})
	if err != nil {
		return err
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
		return err
	}
	return lc.saveChatDeletion(context.WithoutCancel(ctx), chatMid, lastDelivered.MessageID)
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
