package connector

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/event"

	"github.com/beeper/line/pkg/line"
)

const backfillRetryMaxBytes = 1 << 20

func (lc *LineClient) ensureBackfillRetryTable(ctx context.Context) error {
	if lc.backfillRetryReady {
		return nil
	}
	_, err := lc.UserLogin.Bridge.DB.Exec(ctx, `CREATE TABLE IF NOT EXISTS line_backfill_retry (
  bridge_id TEXT NOT NULL,
  login_id TEXT NOT NULL,
  chat_id TEXT NOT NULL,
  message_id TEXT NOT NULL,
  message TEXT NOT NULL,
  expected_parts TEXT NOT NULL DEFAULT '[]',
  PRIMARY KEY (bridge_id, login_id, message_id),
  FOREIGN KEY (bridge_id, login_id) REFERENCES user_login (bridge_id, id) ON DELETE CASCADE
 )`)
	if err == nil {
		lc.backfillRetryReady = true
	}
	return err
}

func (lc *LineClient) storeBackfillRetry(ctx context.Context, chatID string, msg *line.Message) (bool, error) {
	lc.backfillRetryMu.Lock()
	defer lc.backfillRetryMu.Unlock()
	if lc.superseded.Load() {
		return false, errLineClientSuperseded
	}
	if err := lc.ensureBackfillRetryTable(ctx); err != nil {
		return false, err
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return false, err
	}
	if len(payload) > backfillRetryMaxBytes {
		return false, nil
	}
	rows, err := lc.UserLogin.Bridge.DB.Query(ctx, `SELECT message_id, message FROM line_backfill_retry WHERE bridge_id=$1 AND login_id=$2`, lc.UserLogin.Bridge.ID, lc.UserLogin.ID)
	if err != nil {
		return false, err
	}
	used := 0
	for rows.Next() {
		var id, data string
		if err = rows.Scan(&id, &data); err != nil {
			break
		}
		if id != msg.ID {
			used += len(data)
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return false, err
	}
	if used+len(payload) > backfillRetryMaxBytes {
		return false, nil
	}
	// A failed native page cannot grow the retry ledger beyond one page. Overflow
	// becomes a visible unavailable-message notice instead of a silent history hole.
	result, err := lc.UserLogin.Bridge.DB.Exec(ctx, `INSERT INTO line_backfill_retry (bridge_id, login_id, chat_id, message_id, message)
  SELECT $1, $2, $3, $4, $5 WHERE
   (SELECT COUNT(*) FROM line_backfill_retry WHERE bridge_id=$1 AND login_id=$2) < $6 OR
   EXISTS (SELECT 1 FROM line_backfill_retry WHERE bridge_id=$1 AND login_id=$2 AND message_id=$4)
  ON CONFLICT (bridge_id, login_id, message_id) DO UPDATE SET message=excluded.message`,
		lc.UserLogin.Bridge.ID, lc.UserLogin.ID, chatID, msg.ID, string(payload), backfillPageSize)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count > 0, err
}

func (lc *LineClient) deleteBackfillRetry(ctx context.Context, messageID string) error {
	_, err := lc.UserLogin.Bridge.DB.Exec(ctx, `DELETE FROM line_backfill_retry WHERE bridge_id=$1 AND login_id=$2 AND message_id=$3`,
		lc.UserLogin.Bridge.ID, lc.UserLogin.ID, messageID)
	return err
}

func (lc *LineClient) pendingBackfillMessages(ctx context.Context, chatID string) ([]*line.Message, error) {
	rows, err := lc.UserLogin.Bridge.DB.Query(ctx, `SELECT message FROM line_backfill_retry WHERE bridge_id=$1 AND login_id=$2 AND chat_id=$3 ORDER BY message_id LIMIT $4`,
		lc.UserLogin.Bridge.ID, lc.UserLogin.ID, chatID, backfillPageSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []*line.Message
	for rows.Next() {
		var payload string
		if err = rows.Scan(&payload); err != nil {
			return nil, err
		}
		var msg line.Message
		if err = json.Unmarshal([]byte(payload), &msg); err != nil {
			return nil, err
		}
		messages = append(messages, &msg)
	}
	return messages, rows.Err()
}

func (lc *LineClient) queueBackfillRetryChats(ctx context.Context) {
	if ctx.Err() != nil || lc.superseded.Load() {
		return
	}
	lc.backfillRetryMu.Lock()
	if err := lc.ensureBackfillRetryTable(ctx); err != nil {
		lc.backfillRetryMu.Unlock()
		lc.UserLogin.Bridge.Log.Warn().Err(err).Msg("Failed to initialize backfill retries")
		return
	}
	rows, err := lc.UserLogin.Bridge.DB.Query(ctx, `SELECT DISTINCT chat_id FROM line_backfill_retry WHERE bridge_id=$1 AND login_id=$2`, lc.UserLogin.Bridge.ID, lc.UserLogin.ID)
	var chats []string
	if err == nil {
		for rows.Next() {
			var chat string
			if err = rows.Scan(&chat); err != nil {
				break
			}
			chats = append(chats, chat)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
	}
	lc.backfillRetryMu.Unlock()
	if err != nil {
		lc.UserLogin.Bridge.Log.Warn().Err(err).Msg("Failed to load backfill retries")
		return
	}
	for _, chat := range chats {
		if lc.isUserBlocked(chat) {
			continue
		}
		lc.UserLogin.Bridge.QueueRemoteEvent(lc.UserLogin, &simplevent.ChatResync{
			EventMeta:              simplevent.EventMeta{Type: bridgev2.RemoteEventChatResync, PortalKey: networkid.PortalKey{ID: makePortalID(chat), Receiver: lc.UserLogin.ID}},
			CheckNeedsBackfillFunc: alwaysNeedsBackfill,
		})
	}
}

func (lc *LineClient) retryBackfillMessages(ctx context.Context, portal *bridgev2.Portal) {
	lc.backfillRetryMu.Lock()
	if lc.backfillRetryActive[portal.PortalKey] || lc.superseded.Load() {
		lc.backfillRetryMu.Unlock()
		return
	}
	if err := lc.ensureBackfillRetryTable(ctx); err != nil {
		lc.backfillRetryMu.Unlock()
		lc.UserLogin.Bridge.Log.Warn().Err(err).Msg("Failed to initialize backfill retries")
		return
	}
	messages, err := lc.pendingBackfillMessages(ctx, string(portal.ID))
	if lc.backfillRetryActive == nil {
		lc.backfillRetryActive = make(map[networkid.PortalKey]bool)
	}
	lc.backfillRetryActive[portal.PortalKey] = true
	lc.backfillRetryMu.Unlock()
	defer func() {
		lc.backfillRetryMu.Lock()
		delete(lc.backfillRetryActive, portal.PortalKey)
		lc.backfillRetryMu.Unlock()
	}()
	if err != nil {
		lc.UserLogin.Bridge.Log.Warn().Err(err).Msg("Failed to load backfill retries")
		return
	}
	for _, msg := range messages {
		if ctx.Err() != nil || lc.superseded.Load() {
			return
		}
		if lc.isUserBlocked(string(portal.ID)) {
			return
		}
		if lc.shouldSkipDeletedChat(string(portal.ID), msg.ID) {
			if err = lc.deleteBackfillRetry(ctx, msg.ID); err != nil {
				return
			}
			continue
		}
		existing, queryErr := lc.UserLogin.Bridge.DB.Message.GetAllPartsByID(ctx, lc.UserLogin.ID, networkid.MessageID(msg.ID))
		if queryErr != nil {
			return
		}
		parts := make(map[networkid.PartID]bool, len(existing))
		for _, part := range existing {
			parts[part.PartID] = !part.HasFakeMXID()
		}
		var encodedParts string
		if err = lc.UserLogin.Bridge.DB.QueryRow(ctx, `SELECT expected_parts FROM line_backfill_retry WHERE bridge_id=$1 AND login_id=$2 AND message_id=$3`, lc.UserLogin.Bridge.ID, lc.UserLogin.ID, msg.ID).Scan(&encodedParts); err != nil {
			return
		}
		var expected []networkid.PartID
		if err = json.Unmarshal([]byte(encodedParts), &expected); err != nil {
			return
		}
		complete := len(expected) > 0
		for _, partID := range expected {
			complete = complete && parts[partID]
		}
		if !complete {
			converted, convertErr := lc.convertBackfillMessage(ctx, portal, msg)
			if convertErr != nil {
				lc.UserLogin.Bridge.Log.Debug().Err(convertErr).Str("msg_id", msg.ID).Msg("Backfill message is still unavailable")
				continue
			} else if converted != nil {
				expected = nil
				missing := make([]*bridgev2.ConvertedMessagePart, 0, len(converted.Parts))
				for _, part := range converted.Parts {
					expected = append(expected, part.ID)
					if !parts[part.ID] {
						missing = append(missing, part)
					}
				}
				encoded, marshalErr := json.Marshal(expected)
				if marshalErr != nil {
					return
				}
				if _, err = lc.UserLogin.Bridge.DB.Exec(ctx, `UPDATE line_backfill_retry SET expected_parts=$4 WHERE bridge_id=$1 AND login_id=$2 AND message_id=$3`, lc.UserLogin.Bridge.ID, lc.UserLogin.ID, msg.ID, string(encoded)); err != nil {
					return
				}
				if len(missing) > 0 {
					converted.Parts = missing
					// Recovered holes bypass the normal timestamp cutoff. The framework
					// invokes FetchMessages under its existing backfill lock.
					if err = lc.sendRecoveredBackfill(ctx, portal, converted); err != nil {
						lc.UserLogin.Bridge.Log.Warn().Err(err).Str("msg_id", msg.ID).Msg("Failed to send recovered backfill message")
						continue
					}
				}
				complete = true
				for _, partID := range expected {
					part, partErr := lc.UserLogin.Bridge.DB.Message.GetPartByID(ctx, lc.UserLogin.ID, networkid.MessageID(msg.ID), partID)
					if partErr != nil || part == nil || part.HasFakeMXID() {
						complete = false
						break
					}
				}
				if !complete {
					continue
				}
			}
		}
		if err = lc.ensureRecoveredReactions(ctx, portal, msg); err != nil {
			lc.UserLogin.Bridge.Log.Warn().Err(err).Str("msg_id", msg.ID).Msg("Failed to recover historical reactions")
			continue
		}
		if err = lc.deleteBackfillRetry(ctx, msg.ID); err != nil {
			return
		}
	}
}

func (lc *LineClient) convertBackfillMessage(ctx context.Context, portal *bridgev2.Portal, msg *line.Message) (*bridgev2.BackfillMessage, error) {
	sender := lc.eventSenderForMID(msg.From)
	intent, ok := portal.GetIntentFor(ctx, sender, lc.UserLogin, bridgev2.RemoteEventMessage)
	if !ok {
		return nil, fmt.Errorf("failed to get backfill sender intent")
	}
	opType := OpReceiveMessage
	if msg.From == lc.Mid {
		opType = OpSendMessage
	}
	bodyText, unwrappedText, decryptionFailed := lc.decryptMessageBody(msg, string(portal.ID), int(opType))
	converted, err := lc.convertLineMessage(ctx, portal, intent, *msg, bodyText, unwrappedText, decryptionFailed)
	if err != nil || converted == nil {
		return nil, err
	}
	reactions, _ := lc.convertMessageReactions(ctx, msg)
	return &bridgev2.BackfillMessage{ConvertedMessage: converted, Sender: sender, ID: networkid.MessageID(msg.ID), Timestamp: lc.parseMessageTimestamp(msg), Reactions: reactions}, nil
}

func (lc *LineClient) backfillRetryOverflow(msg *line.Message) *bridgev2.BackfillMessage {
	return &bridgev2.BackfillMessage{
		ID: networkid.MessageID(msg.ID), Sender: lc.eventSenderForMID(msg.From), Timestamp: lc.parseMessageTimestamp(msg),
		ConvertedMessage: &bridgev2.ConvertedMessage{Parts: []*bridgev2.ConvertedMessagePart{{Type: event.EventMessage, Content: &event.MessageEventContent{
			MsgType: event.MsgNotice, Body: "[LINE message unavailable. Open LINE to view it.]",
		}}}},
	}
}

func (lc *LineClient) discardBackfillRetry(ctx context.Context, messageID string) {
	lc.backfillRetryMu.Lock()
	defer lc.backfillRetryMu.Unlock()
	if err := lc.ensureBackfillRetryTable(ctx); err != nil {
		lc.UserLogin.Bridge.Log.Warn().Err(err).Msg("Failed to invalidate pending backfill message")
		return
	}
	if err := lc.deleteBackfillRetry(ctx, messageID); err != nil {
		lc.UserLogin.Bridge.Log.Warn().Err(err).Msg("Failed to invalidate pending backfill message")
	}
}

type backfillRetryIntent struct {
	bridgev2.MatrixAPI
	loginID networkid.UserLoginID
}

func (intent backfillRetryIntent) SendMessage(ctx context.Context, roomID id.RoomID, eventType event.Type, content *event.Content, extra *bridgev2.MatrixSendExtra) (*mautrix.RespSendEvent, error) {
	if extra != nil && extra.MessageMeta != nil {
		copy := *extra
		copy.TransactionID = fmt.Sprintf("line-history-retry-%x", sha256.Sum256([]byte(fmt.Sprintf("%s/%s/%s/%s", intent.loginID, roomID, extra.MessageMeta.ID, extra.MessageMeta.PartID))))
		extra = &copy
	} else if extra != nil && extra.ReactionMeta != nil {
		copy := *extra
		copy.TransactionID = fmt.Sprintf("line-history-retry-rx-%x", sha256.Sum256([]byte(fmt.Sprintf("%s/%s/%s/%s/%s/%s", intent.loginID, roomID, extra.ReactionMeta.MessageID, extra.ReactionMeta.MessagePartID, extra.ReactionMeta.SenderID, extra.ReactionMeta.EmojiID))))
		extra = &copy
	}
	return intent.MatrixAPI.SendMessage(ctx, roomID, eventType, content, extra)
}
func (lc *LineClient) sendRecoveredBackfill(ctx context.Context, portal *bridgev2.Portal, msg *bridgev2.BackfillMessage) error {
	if portal.Bridge.Matrix.GetCapabilities().BatchSending {
		return portal.Internal().SendBackfill(ctx, lc.UserLogin, []*bridgev2.BackfillMessage{msg}, false, false, false, nil)
	}
	intent, ok := portal.GetIntentFor(ctx, msg.Sender, lc.UserLogin, bridgev2.RemoteEventMessage)
	if !ok {
		return fmt.Errorf("failed to get retry sender intent")
	}
	_, res := portal.Internal().SendConvertedMessage(ctx, lc.UserLogin, msg.ID, backfillRetryIntent{MatrixAPI: intent, loginID: lc.UserLogin.ID}, msg.Sender.Sender, msg.ConvertedMessage, msg.Timestamp, msg.StreamOrder, nil, true)
	if !res.Success || res.Queued {
		if res.Error != nil {
			return res.Error
		}
		return fmt.Errorf("recovered backfill message was not delivered")
	}
	return nil
}

func (lc *LineClient) ensureRecoveredReactions(ctx context.Context, portal *bridgev2.Portal, msg *line.Message) error {
	if msg.Reactions == nil {
		return nil
	}
	reactions, complete := lc.convertMessageReactions(ctx, msg)
	if !complete {
		return fmt.Errorf("historical reaction conversion is incomplete")
	}
	for _, reaction := range reactions {
		target, err := lc.UserLogin.Bridge.DB.Message.GetFirstOrSpecificPartByID(ctx, lc.UserLogin.ID, networkid.MessageOptionalPartID{MessageID: networkid.MessageID(msg.ID), PartID: reaction.TargetPart})
		if err != nil {
			return err
		} else if target == nil {
			return fmt.Errorf("historical reaction target is unavailable")
		}
		existing, err := lc.UserLogin.Bridge.DB.Reaction.GetByID(ctx, lc.UserLogin.ID, target.ID, target.PartID, reaction.Sender.Sender, reaction.EmojiID)
		if err != nil {
			return err
		} else if existing != nil {
			continue
		}
		intent, ok := portal.GetIntentFor(ctx, reaction.Sender, lc.UserLogin, bridgev2.RemoteEventReaction)
		if !ok {
			return fmt.Errorf("failed to get historical reaction sender intent")
		}
		res := portal.Internal().SendConvertedReaction(ctx, reaction.Sender.Sender, backfillRetryIntent{MatrixAPI: intent, loginID: lc.UserLogin.ID}, target, reaction.EmojiID, reaction.Emoji, reaction.Timestamp, reaction.DBMetadata, reaction.ExtraContent, nil)
		if !res.Success || res.Queued {
			if res.Error != nil {
				return res.Error
			}
			return fmt.Errorf("historical reaction was not delivered")
		}
	}
	return nil
}
