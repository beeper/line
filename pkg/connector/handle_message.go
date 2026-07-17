package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/highesttt/matrix-line-messenger/pkg/connector/handlers"
	"github.com/highesttt/matrix-line-messenger/pkg/e2ee"
	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

const (
	lineDecryptFallbackText      = "[Unable to decrypt message. Open an issue on GitHub.]"
	lineDecryptFailureNoticeText = "[Unable to decrypt LINE message.]"
)

func (lc *LineClient) newMessageHandler() *handlers.Handler {
	return &handlers.Handler{
		Log:               lc.UserLogin.Bridge.Log,
		HTTPClient:        lc.HTTPClient,
		RecoverToken:      lc.recoverToken,
		ShouldRecover:     lc.shouldAttemptTokenRecovery,
		IsRefreshRequired: lc.isRefreshRequired,
		IsLoggedOut:       lc.isLoggedOut,
		HandleLoggedOut:   lc.markLoggedOutByOtherClient,
		NewClient:         func() *line.Client { return lc.newClient() },
		DecryptMedia:      lc.decryptImageData,
	}
}

func e2eeChunkLengths(chunks []string) []int {
	lengths := make([]int, len(chunks))
	for i, chunk := range chunks {
		lengths[i] = len(chunk)
	}
	return lengths
}

func messageDecryptLogContext(evt *zerolog.Event, msg *line.Message, chatMID string, opType int, finalKeyIDField string) *zerolog.Event {
	evt = evt.
		Str("msg_id", msg.ID).
		Str("chat_mid", chatMID).
		Str("from", msg.From).
		Str("to", msg.To).
		Int("to_type", msg.ToType).
		Int("op_type", opType).
		Int("content_type", msg.ContentType).
		Int("chunk_count", len(msg.Chunks)).
		Ints("chunk_lengths", e2eeChunkLengths(msg.Chunks))

	if version := msg.ContentMetadata["e2eeVersion"]; version != "" {
		evt = evt.Str("e2ee_version", version)
	}
	if len(msg.Chunks) >= 5 {
		if senderKeyID, err := e2ee.DecodeKeyID(msg.Chunks[len(msg.Chunks)-2]); err == nil {
			evt = evt.Int("sender_key_id", senderKeyID)
		} else {
			evt = evt.Str("sender_key_id_error", err.Error())
		}
		if finalKeyID, err := e2ee.DecodeKeyID(msg.Chunks[len(msg.Chunks)-1]); err == nil {
			evt = evt.Int(finalKeyIDField, finalKeyID)
		} else {
			evt = evt.Str(finalKeyIDField+"_error", err.Error())
		}
	}

	return evt
}

func groupDecryptLogContext(evt *zerolog.Event, msg *line.Message, chatMID string, opType int) *zerolog.Event {
	return messageDecryptLogContext(evt, msg, chatMID, opType, "group_key_id")
}

func directDecryptLogContext(evt *zerolog.Event, msg *line.Message, chatMID string, opType int) *zerolog.Event {
	return messageDecryptLogContext(evt, msg, chatMID, opType, "receiver_key_id")
}

type messageWithChatInfo struct {
	*simplevent.Message[line.Message]
	GetChatInfoFunc func(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error)
}

var _ bridgev2.RemoteChatResyncWithInfo = (*messageWithChatInfo)(nil)

func (evt *messageWithChatInfo) GetChatInfo(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
	if evt.GetChatInfoFunc == nil {
		return nil, nil
	}
	return evt.GetChatInfoFunc(ctx, portal)
}

func (lc *LineClient) getChatInfoForIncomingMessage(ctx context.Context, portal *bridgev2.Portal, chatMid string) (*bridgev2.ChatInfo, error) {
	info, err := lc.GetChatInfo(ctx, portal)
	if err != nil {
		return nil, err
	}
	if isChatMID(chatMid) {
		lc.stripRemoteMembersFromInitialChatInfo(info)
	}
	return info, nil
}

func (lc *LineClient) queueIncomingMessage(msg *line.Message, opType int) {
	// Only process known content types; skip system messages (group created, member invited, etc.)
	if !isBridgeableContentType(msg) {
		lc.UserLogin.Bridge.Log.Debug().
			Int("content_type", msg.ContentType).
			Str("msg_id", msg.ID).
			Interface("content_metadata", msg.ContentMetadata).
			Str("text", msg.Text).
			Int("chunk_count", len(msg.Chunks)).
			Msg("Skipping unsupported content type")
		return
	}

	portalIDStr := portalMIDForMessage(msg, opType)
	portalKey := networkid.PortalKey{ID: makePortalID(portalIDStr), Receiver: lc.UserLogin.ID}
	ts := lc.parseMessageTimestamp(msg)

	lc.ensureGroupMessageSenderKnown(portalIDStr, msg.From, ts)

	senderID := makeUserID(msg.From)
	bodyText, unwrappedText, decryptionFailed := lc.decryptMessageBody(msg, portalIDStr, opType)

	messageEvent := &simplevent.Message[line.Message]{
		EventMeta: simplevent.EventMeta{
			Type:         bridgev2.RemoteEventMessage,
			LogContext:   func(c zerolog.Context) zerolog.Context { return c.Str("msg_id", msg.ID) },
			PortalKey:    portalKey,
			CreatePortal: true,
			Sender:       bridgev2.EventSender{Sender: senderID, IsFromMe: OperationType(opType) == OpSendMessage},
			Timestamp:    ts,
			PreHandleFunc: func(ctx context.Context, portal *bridgev2.Portal) {
				lc.hiddenJoinGroupMessageSender(ctx, portal, portalIDStr, msg.From, ts)
			},
		},
		Data: *msg,
		ID:   networkid.MessageID(msg.ID),
		ConvertMessageFunc: func(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, data line.Message) (*bridgev2.ConvertedMessage, error) {
			return lc.convertLineMessage(ctx, portal, intent, data, bodyText, unwrappedText, decryptionFailed)
		},
	}

	var remoteEvent bridgev2.RemoteEvent = messageEvent
	if isChatMID(portalIDStr) {
		remoteEvent = &messageWithChatInfo{
			Message: messageEvent,
			GetChatInfoFunc: func(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
				return lc.getChatInfoForIncomingMessage(ctx, portal, portalIDStr)
			},
		}
	}

	lc.UserLogin.Bridge.QueueRemoteEvent(lc.UserLogin, remoteEvent)
}

// isBridgeableContentType reports whether an inbound LINE message should be
// bridged to Matrix. System messages (group created, member invited, etc.) are
// skipped, but call and contact notifications are let through regardless of
// content type because LINE may wrap them in non-standard content type values.
func isBridgeableContentType(msg *line.Message) bool {
	switch ContentType(msg.ContentType) {
	case ContentText, ContentImage, ContentVideo, ContentAudio,
		ContentSticker, ContentContact, ContentFile, ContentLocation, ContentFlex:
		return true
	default:
		return msg.ContentMetadata["ORGCONTP"] == "CALL" || msg.ContentMetadata["ORGCONTP"] == "CONTACT"
	}
}

// portalMIDForMessage returns the chat MID that owns a message (the portal key).
func portalMIDForMessage(msg *line.Message, opType int) string {
	portalIDStr := msg.From
	// If I sent it (Type 25), the portal is the recipient (msg.To)
	if OperationType(opType) == OpSendMessage {
		portalIDStr = msg.To
	}
	// If it's a group (ToType 1 or 2), the portal is msg.To
	if ToType(msg.ToType) == ToRoom || ToType(msg.ToType) == ToGroup {
		portalIDStr = msg.To
	}
	return portalIDStr
}

// parseMessageTimestamp converts a LINE message's CreatedTime to a time.Time,
// falling back to the current time if it can't be parsed or is zero.
func (lc *LineClient) parseMessageTimestamp(msg *line.Message) time.Time {
	tsInt, err := msg.CreatedTime.Int64()
	if err != nil {
		lc.UserLogin.Bridge.Log.Warn().
			Err(err).
			Str("msg_id", msg.ID).
			Msg("Failed to convert message CreatedTime to int64, using current time")
		return time.Now()
	}
	// time.UnixMilli(0) is the Unix epoch, not Go's zero time, so IsZero() never
	// catches a missing timestamp — guard on the raw value instead.
	if tsInt == 0 {
		return time.Now()
	}
	return time.UnixMilli(tsInt)
}

// decryptMessageBody runs E2EE decryption (when needed) for an inbound message
// and returns the plaintext body plus the JSON-unwrapped text. Shared by the
// live message path (queueIncomingMessage) and the backfill path (FetchMessages).
func (lc *LineClient) decryptMessageBody(msg *line.Message, portalIDStr string, opType int) (bodyText, unwrappedText string, decryptionFailed bool) {
	// Handle Content
	bodyText = msg.Text
	if len(msg.Chunks) > 0 && (bodyText == "" || isLineDecryptFallbackText(bodyText)) {
		bodyText = ""
		decryptionFailed = true
		if lc.E2EE != nil {
			// Ensure peer keys are available before attempting decryption
			lc.ensurePeerKeyForMessage(context.Background(), msg)

			// If we receive an encrypted group message, clear its noE2EE cache
			// so future sends will attempt E2EE again.
			if (ToType(msg.ToType) == ToRoom || ToType(msg.ToType) == ToGroup) && lc.isGroupNoE2EE(portalIDStr) {
				lc.UserLogin.Bridge.Log.Info().Str("chat_mid", portalIDStr).Msg("Received encrypted group message, clearing noE2EE cache")
				lc.clearGroupNoE2EE(portalIDStr)
			}

			if ToType(msg.ToType) == ToRoom || ToType(msg.ToType) == ToGroup {
				// Group Decryption
				if len(msg.Chunks) >= 5 {
					if gkID, err := e2ee.DecodeKeyID(msg.Chunks[len(msg.Chunks)-1]); err == nil && gkID != 0 {
						if errFetch := lc.fetchAndUnwrapGroupKey(context.Background(), portalIDStr, gkID); errFetch != nil {
							groupDecryptLogContext(lc.UserLogin.Bridge.Log.Debug().Err(errFetch), msg, portalIDStr, opType).
								Msg("Prefetch group key before decrypt failed")
						}
					}
				}

				pt, keyID, err := lc.E2EE.DecryptGroupMessage(msg, portalIDStr)
				if err == nil {
					bodyText = pt
					decryptionFailed = false
				} else {
					groupDecryptLogContext(lc.UserLogin.Bridge.Log.Debug().Err(err), msg, portalIDStr, opType).
						Msg("DecryptGroupMessage failed, trying to fetch key")
					if keyID != 0 {
						if errFetch := lc.fetchAndUnwrapGroupKey(context.Background(), portalIDStr, keyID); errFetch != nil {
							groupDecryptLogContext(lc.UserLogin.Bridge.Log.Warn().Err(errFetch), msg, portalIDStr, opType).
								Msg("Failed to fetch/unwrap group key")
						} else if ptRetry, _, errRetry := lc.E2EE.DecryptGroupMessage(msg, portalIDStr); errRetry == nil {
							bodyText = ptRetry
							decryptionFailed = false
						} else {
							groupDecryptLogContext(lc.UserLogin.Bridge.Log.Warn().Err(errRetry), msg, portalIDStr, opType).
								Msg("DecryptGroupMessage failed after group key refresh")
						}
					}
				}
			} else {
				// 1-1 Decryption
				if pt, err := lc.E2EE.DecryptMessageV2(msg); err == nil {
					bodyText = pt
					decryptionFailed = false
				} else {
					directDecryptLogContext(lc.UserLogin.Bridge.Log.Debug().Err(err), msg, portalIDStr, opType).
						Msg("DecryptMessageV2 failed on first attempt")
					if _, _, errKey := lc.E2EE.MyKeyIDs(); errKey != nil {
						directDecryptLogContext(lc.UserLogin.Bridge.Log.Error().Err(errKey), msg, portalIDStr, opType).
							Msg("E2EE own key not loaded; cannot decrypt any messages. Re-login required")
						lc.markMissingE2EEKey(context.Background(), fmt.Errorf("%w: %v", e2ee.ErrMissingOwnPrivateKey, errKey))
					} else {
						peerMid := msg.From
						peerKeyID := 0
						messageFromMe := peerMid == lc.Mid || peerMid == string(lc.UserLogin.ID)
						if messageFromMe {
							peerMid = msg.To
						}
						// Fetch the EXACT keyID the message used (handles peer key rotation)
						// before falling back to negotiating the peer's current key.
						fetched := false
						if len(msg.Chunks) >= 5 {
							senderKeyID, errSender := e2ee.DecodeKeyID(msg.Chunks[len(msg.Chunks)-2])
							receiverKeyID, errReceiver := e2ee.DecodeKeyID(msg.Chunks[len(msg.Chunks)-1])
							if errSender == nil {
								peerKeyID = senderKeyID
							}
							if messageFromMe && errReceiver == nil {
								peerKeyID = receiverKeyID
							}
							if peerKeyID != 0 {
								if _, _, errPeer := lc.ensurePeerKeyByID(context.Background(), peerMid, peerKeyID); errPeer == nil {
									fetched = true
								} else {
									directDecryptLogContext(lc.UserLogin.Bridge.Log.Debug().Err(errPeer), msg, portalIDStr, opType).
										Str("peer", peerMid).
										Int("key_id", peerKeyID).
										Msg("ensurePeerKeyByID failed on retry, falling back to NegotiateE2EEPublicKey")
								}
							}
						}
						if !fetched {
							if _, _, errPeer := lc.ensurePeerKey(context.Background(), peerMid); errPeer != nil {
								directDecryptLogContext(lc.UserLogin.Bridge.Log.Warn().Err(errPeer), msg, portalIDStr, opType).
									Str("peer", peerMid).
									Msg("Failed to force-fetch peer key for retry")
							}
						}
						if ptRetry, errRetry := lc.E2EE.DecryptMessageV2(msg); errRetry == nil {
							bodyText = ptRetry
							decryptionFailed = false
						} else {
							directDecryptLogContext(lc.UserLogin.Bridge.Log.Warn().Err(errRetry), msg, portalIDStr, opType).
								Msg("DecryptMessageV2 failed on retry")
							lc.markMissingE2EEKey(context.Background(), errRetry)
						}
					}
				}
			}
		}
	}

	// unwrap JSON payload
	unwrappedText = bodyText
	if strings.HasPrefix(bodyText, "{") {
		var wrapper map[string]any
		if err := json.Unmarshal([]byte(bodyText), &wrapper); err == nil {
			if t, ok := wrapper["text"].(string); ok {
				unwrappedText = t
			}
		}
	}
	return bodyText, unwrappedText, decryptionFailed
}

func isLineDecryptFallbackText(text string) bool {
	// LINE may include this historical fallback in Text while still sending
	// encrypted chunks. Treat it as a decrypt marker, not user-authored text.
	return strings.TrimSpace(text) == lineDecryptFallbackText
}

// convertLineMessage converts an inbound LINE message into a Matrix
// ConvertedMessage. bodyText/unwrappedText are the (decrypted) message text as
// returned by decryptMessageBody. Shared by the live message path and backfill.
func (lc *LineClient) convertLineMessage(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, data line.Message, bodyText, unwrappedText string, decryptionFailed bool) (*bridgev2.ConvertedMessage, error) {
	decryptedBody := bodyText
	replyRelatesTo := lc.resolveReplyRelatesTo(ctx, &data)

	if decryptionFailed && strings.TrimSpace(unwrappedText) == "" && ContentType(data.ContentType) == ContentText {
		return &bridgev2.ConvertedMessage{
			Parts: []*bridgev2.ConvertedMessagePart{
				{
					Type: event.EventMessage,
					Content: &event.MessageEventContent{
						MsgType:   event.MsgNotice,
						Body:      lineDecryptFailureNoticeText,
						RelatesTo: replyRelatesTo,
					},
				},
			},
		}, nil
	}

	h := lc.newMessageHandler()

	// Handle call events (ORGCONTP == "CALL")
	if data.ContentMetadata["ORGCONTP"] == "CALL" {
		return h.ConvertCall(data, replyRelatesTo)
	}

	// Dispatch to content-type-specific handlers
	switch ContentType(data.ContentType) {
	case ContentImage:
		return h.ConvertImage(ctx, portal, intent, data, decryptedBody, replyRelatesTo)
	case ContentVideo:
		return h.ConvertVideo(ctx, portal, intent, data, decryptedBody, replyRelatesTo)
	case ContentAudio:
		return h.ConvertAudio(ctx, portal, intent, data, decryptedBody, replyRelatesTo)
	case ContentFile:
		return h.ConvertFile(ctx, portal, intent, data, decryptedBody, replyRelatesTo)
	case ContentSticker:
		return h.ConvertSticker(ctx, portal, intent, data, replyRelatesTo)
	case ContentLocation:
		return h.ConvertLocation(data, replyRelatesTo)
	case ContentContact:
		return h.ConvertContact(data, replyRelatesTo)
	case ContentFlex:
		return h.ConvertFlex(data, replyRelatesTo)
	}

	// Handle device/phone contact shared via ORGCONTP (contentType 0 with vCard)
	if data.ContentMetadata["ORGCONTP"] == "CONTACT" {
		return h.ConvertDeviceContact(ctx, portal, intent, data, unwrappedText, replyRelatesTo)
	}

	var converted *bridgev2.ConvertedMessage
	var err error

	// Handle inline emoji/stamp embedded in text messages
	if data.ContentMetadata["STKID"] != "" || data.ContentMetadata["STKPKGID"] != "" ||
		data.ContentMetadata["STICON_OWNERSHIP"] != "" ||
		handlers.HasSticonBody(bodyText) ||
		handlers.ContainsLineSticonPlaceholder(unwrappedText) {
		if data.ContentMetadata["STICON_OWNERSHIP"] != "" {
			h.Log.Debug().
				Int("body_length", len(bodyText)).
				Int("unwrapped_length", len(unwrappedText)).
				Int("metadata_count", len(data.ContentMetadata)).
				Msg("STICON_OWNERSHIP: full message body")
		}
		converted, err = h.ConvertInlineEmoji(ctx, portal, intent, data, unwrappedText, bodyText, replyRelatesTo)
	} else {
		// Skip empty/whitespace-only text messages (system messages that fell through)
		if strings.TrimSpace(unwrappedText) == "" {
			return nil, nil
		}

		// Default to text
		converted, err = h.ConvertText(unwrappedText, replyRelatesTo)
	}
	if err != nil {
		return nil, err
	}

	if mentionStr := data.ContentMetadata["MENTION"]; mentionStr != "" && converted != nil && len(converted.Parts) > 0 && converted.Parts[0].Content != nil {
		lc.UserLogin.Bridge.Log.Debug().Str("raw_mention", mentionStr).Msg("Processing inbound LINE MENTION metadata")
		canFormatMentions := converted.Parts[0].Content.Body == unwrappedText && converted.Parts[0].Content.FormattedBody == ""
		var mentionData struct {
			MENTIONEES []struct {
				M string `json:"M,omitempty"`
				A string `json:"A,omitempty"`
				S string `json:"S,omitempty"`
				E string `json:"E,omitempty"`
			} `json:"MENTIONEES"`
		}
		if err := json.Unmarshal([]byte(mentionStr), &mentionData); err != nil {
			lc.UserLogin.Bridge.Log.Debug().Err(err).Msg("Failed to unmarshal MENTION metadata")
		} else {
			ghostFormatter, ok := lc.UserLogin.Bridge.Matrix.(interface {
				FormatGhostMXID(networkid.UserID) id.UserID
			})
			lc.UserLogin.Bridge.Log.Debug().Bool("formatter_ok", ok).Msg("Checking FormatGhostMXID availability")
			mentions := &event.Mentions{}
			type mentionEntry struct {
				start int
				end   int
				mxid  string
			}
			var entries []mentionEntry
			for _, ment := range mentionData.MENTIONEES {
				lc.UserLogin.Bridge.Log.Debug().
					Str("ment_mid", ment.M).
					Str("ment_a", ment.A).
					Str("ment_s", ment.S).
					Str("ment_e", ment.E).
					Bool("has_formatter", ok).
					Msg("Processing mention entry")
				if ment.M != "" {
					var mxid id.UserID
					switch {
					case ment.M == lc.Mid || ment.M == string(lc.UserLogin.ID):
						mxid = lc.UserLogin.UserMXID
						lc.UserLogin.Bridge.Log.Debug().Str("mxid", string(mxid)).Msg("Mention targets bridge user, using real MXID")
					case ok:
						mxid = ghostFormatter.FormatGhostMXID(networkid.UserID(ment.M))
					default:
						lc.UserLogin.Bridge.Log.Debug().Msg("Skip mention: unknown MID and no formatter available")
						continue
					}
					lc.UserLogin.Bridge.Log.Debug().Str("mxid", string(mxid)).Msg("Formatted MXID from LINE MID")
					mentions.UserIDs = append(mentions.UserIDs, mxid)
					if canFormatMentions {
						if s, errS := strconv.Atoi(ment.S); errS == nil && s >= 0 {
							if e, errE := strconv.Atoi(ment.E); errE == nil && e <= len(unwrappedText) && e > s {
								entries = append(entries, mentionEntry{start: s, end: e, mxid: string(mxid)})
							}
						}
					}
				}
				if ment.A == "1" {
					mentions.Room = true
					if canFormatMentions {
						if s, errS := strconv.Atoi(ment.S); errS == nil && s >= 0 {
							if e, errE := strconv.Atoi(ment.E); errE == nil && e <= len(unwrappedText) && e > s {
								entries = append(entries, mentionEntry{start: s, end: e, mxid: "@room"})
							}
						}
					}
				}
			}
			if len(mentions.UserIDs) > 0 || mentions.Room {
				logEvt := lc.UserLogin.Bridge.Log.Debug().
					Int("user_count", len(mentions.UserIDs)).
					Bool("is_room", mentions.Room)
				if len(entries) > 0 {
					logEvt = logEvt.Int("formatted_body_entries", len(entries))
				}
				logEvt.Msg("Setting mentions on converted message")
				var formattedBody string
				if len(entries) > 0 {
					sort.Slice(entries, func(i, j int) bool { return entries[i].start < entries[j].start })
					var fb strings.Builder
					lastEnd := 0
					for _, entry := range entries {
						if entry.start >= lastEnd && entry.start >= 0 && entry.end <= len(unwrappedText) && entry.start < entry.end {
							fb.WriteString(html.EscapeString(unwrappedText[lastEnd:entry.start]))
							fb.WriteString(fmt.Sprintf(`<a href="https://matrix.to/#/%s">%s</a>`, html.EscapeString(entry.mxid), html.EscapeString(unwrappedText[entry.start:entry.end])))
							lastEnd = entry.end
						}
					}
					fb.WriteString(html.EscapeString(unwrappedText[lastEnd:]))
					formattedBody = fb.String()
				}
				// Replace room mention text in body with @room for client-side highlighting.
				// Process end-to-start to preserve positions for earlier entries.
				if mentions.Room && len(entries) > 0 {
					body := converted.Parts[0].Content.Body
					for i := len(entries) - 1; i >= 0; i-- {
						if entries[i].mxid == "@room" && entries[i].start >= 0 && entries[i].end <= len(body) {
							body = body[:entries[i].start] + "@room" + body[entries[i].end:]
						}
					}
					for _, part := range converted.Parts {
						part.Content.Body = body
					}
				}
				for _, part := range converted.Parts {
					part.Content.Mentions = mentions
					if formattedBody != "" {
						part.Content.Format = event.FormatHTML
						part.Content.FormattedBody = formattedBody
					}
				}
			}
		}
	}

	return converted, nil
}

// resolveReplyRelatesTo looks up the Matrix event ID for a replied-to LINE message.
func (lc *LineClient) resolveReplyRelatesTo(ctx context.Context, data *line.Message) *event.RelatesTo {
	if data == nil {
		return nil
	}

	relatedID := data.RelatedMessageID
	if relatedID == "" && data.ContentMetadata != nil {
		relatedID = data.ContentMetadata["message_relation_server_message_id"]
	}

	if relatedID == "" {
		return nil
	}

	if data.MessageRelationType != 0 && data.MessageRelationType != 3 {
		return nil
	}

	dbMsg, err := lc.UserLogin.Bridge.DB.Message.GetPartByID(ctx, lc.UserLogin.ID, networkid.MessageID(relatedID), "")
	if err != nil {
		lc.UserLogin.Bridge.Log.Debug().Err(err).Str("related_msg_id", relatedID).Msg("Failed to lookup reply target")
		return nil
	}
	if dbMsg == nil || dbMsg.MXID == "" {
		lc.UserLogin.Bridge.Log.Debug().Str("related_msg_id", relatedID).Msg("No Matrix event found for reply target")
		return nil
	}

	return &event.RelatesTo{InReplyTo: &event.InReplyTo{EventID: dbMsg.MXID}}
}
