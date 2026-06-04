package connector

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

const (
	lineOriginalEmojiProductID = "670e0cce840a8236ddd4ee4c"
	lineTrialEmojiProductID    = "5ac1bfd5040ab15980c9b435"
	maxLineReqSeq              = 1_000_000_000
	sentReqSeqTTL              = 5 * time.Minute
)

type linePaidReactionRef struct {
	ProductID    string
	EmojiID      string
	ResourceType int
	Version      int
}

func (ref linePaidReactionRef) networkEmojiID() networkid.EmojiID {
	return networkid.EmojiID("paid:" + ref.ProductID + ":" + ref.EmojiID)
}

func (ref linePaidReactionRef) reactionType() line.ReactionType {
	return line.ReactionType{
		PaidReactionType: &line.PaidReactionType{
			ProductID:    ref.ProductID,
			EmojiID:      ref.EmojiID,
			ResourceType: ref.ResourceType,
			Version:      ref.Version,
		},
	}
}

// These are the LINE emoji/sticon URLs from the issue's pack-based reaction
// set. Add more entries here as more Matrix emoji -> LINE CDN URL mappings are
// captured.
var lineEmojiReactionURLs = map[string]string{
	"\U0001F40D": lineSticonURL(lineOriginalEmojiProductID, "064"),
	"\U0001F43C": lineSticonURL(lineOriginalEmojiProductID, "068"),
	"\U0001F642": lineSticonURL(lineOriginalEmojiProductID, "077"),
	"\U0001F60A": lineSticonURL(lineOriginalEmojiProductID, "078"),
	"\U0001F604": lineSticonURL(lineOriginalEmojiProductID, "079"),
	"\U0001F60D": lineSticonURL(lineTrialEmojiProductID, "001"),
	"\U0001F606": lineSticonURL(lineTrialEmojiProductID, "002"),
	"\U0001F60C": lineSticonURL(lineTrialEmojiProductID, "012"),
	"\U0001F602": lineSticonURL(lineOriginalEmojiProductID, "080"),
	"\U0001F979": lineSticonURL(lineOriginalEmojiProductID, "081"),
	"\U0001F632": lineSticonURL(lineTrialEmojiProductID, "029"),
	"\U0001F611": lineSticonURL(lineTrialEmojiProductID, "036"),
	"\U0001F61A": lineSticonURL(lineOriginalEmojiProductID, "082"),
	"\U0001F607": lineSticonURL(lineOriginalEmojiProductID, "083"),
	"\U0001F970": lineSticonURL(lineOriginalEmojiProductID, "084"),
	"\U0001F609": lineSticonURL(lineTrialEmojiProductID, "011"),
	"\U0001F61D": lineSticonURL(lineOriginalEmojiProductID, "085"),
	"\U0001F60E": lineSticonURL(lineOriginalEmojiProductID, "086"),
	"\U0001F97A": lineSticonURL(lineOriginalEmojiProductID, "087"),
	"\U0001F641": lineSticonURL(lineOriginalEmojiProductID, "088"),
	"\U0001F62E": lineSticonURL(lineOriginalEmojiProductID, "089"),
	"\U0001F627": lineSticonURL(lineOriginalEmojiProductID, "090"),
	"\U0001F622": lineSticonURL(lineOriginalEmojiProductID, "092"),
	"\U0001F62D": lineSticonURL(lineOriginalEmojiProductID, "093"),
	"\U0001F620": lineSticonURL(lineOriginalEmojiProductID, "094"),
	"\U0001F635": lineSticonURL(lineOriginalEmojiProductID, "095"),
	"\U0001F616": lineSticonURL(lineTrialEmojiProductID, "129"),
	"\U0001F624": lineSticonURL(lineTrialEmojiProductID, "135"),
	"\U0001F613": lineSticonURL(lineOriginalEmojiProductID, "097"),
	"\U0001F60F": lineSticonURL(lineOriginalEmojiProductID, "098"),
	"\U0001F612": lineSticonURL(lineTrialEmojiProductID, "141"),
	"\U0001FAE8": lineSticonURL(lineTrialEmojiProductID, "142"),
	"\U0001F978": lineSticonURL(lineTrialEmojiProductID, "146"),
	"\U0001F605": lineSticonURL(lineOriginalEmojiProductID, "099"),
	"\U0001F633": lineSticonURL(lineOriginalEmojiProductID, "100"),
	"\U0001F631": lineSticonURL(lineOriginalEmojiProductID, "101"),
	"\U0001F972": lineSticonURL(lineOriginalEmojiProductID, "102"),
	"\U0001F62A": lineSticonURL(lineOriginalEmojiProductID, "103"),
	"\U0001F924": lineSticonURL(lineOriginalEmojiProductID, "104"),
	"\U0001F971": lineSticonURL(lineOriginalEmojiProductID, "105"),
	"\U0001F92E": lineSticonURL(lineOriginalEmojiProductID, "107"),
	"\U0001F637": lineSticonURL(lineOriginalEmojiProductID, "108"),
	"\U0001F621": lineSticonURL(lineOriginalEmojiProductID, "109"),
	"\U0001F608": lineSticonURL(lineOriginalEmojiProductID, "110"),
	"\U0001F914": lineSticonURL(lineOriginalEmojiProductID, "118"),
	"\U0001FAE0": lineSticonURL(lineOriginalEmojiProductID, "125"),
	"\U0001F44D": lineSticonURL(lineOriginalEmojiProductID, "143"),
	"\U0001F44E": lineSticonURL(lineOriginalEmojiProductID, "144"),
	"\U0001F91E": lineSticonURL(lineOriginalEmojiProductID, "145"),
	"\u270C":     lineSticonURL(lineOriginalEmojiProductID, "146"),
	"\U0001F442": lineSticonURL(lineTrialEmojiProductID, "246"),
	"\U0001F443": lineSticonURL(lineTrialEmojiProductID, "245"),
	"\U0001F444": lineSticonURL(lineTrialEmojiProductID, "247"),
	"\U0001F44B": lineSticonURL(lineOriginalEmojiProductID, "147"),
	"\U0001F64F": lineSticonURL(lineOriginalEmojiProductID, "148"),
	"\U0001F4AA": lineSticonURL(lineOriginalEmojiProductID, "149"),
	"\U0001FAF6": lineSticonURL(lineOriginalEmojiProductID, "150"),
	"\U0001F448": lineSticonURL(lineOriginalEmojiProductID, "151"),
	"\U0001F449": lineSticonURL(lineOriginalEmojiProductID, "152"),
	"\U0001F918": lineSticonURL(lineOriginalEmojiProductID, "153"),
	"\U0001F44C": lineSticonURL(lineOriginalEmojiProductID, "154"),
	"\U0001F44A": lineSticonURL(lineOriginalEmojiProductID, "155"),
	"\U0001FAF0": lineSticonURL(lineOriginalEmojiProductID, "156"),
	"\U0001F431": lineSticonURL(lineOriginalEmojiProductID, "157"),
	"\U0001F436": lineSticonURL(lineOriginalEmojiProductID, "158"),
	"\U0001F385": lineSticonURL(lineOriginalEmojiProductID, "159"),
	"\U0001F47B": lineSticonURL(lineOriginalEmojiProductID, "160"),
	"\U0001F921": lineSticonURL(lineOriginalEmojiProductID, "161"),
	"\U0001F47D": lineSticonURL(lineOriginalEmojiProductID, "162"),
	"\U0001F4A9": lineSticonURL(lineOriginalEmojiProductID, "163"),
	"\U0001F4B0": lineSticonURL(lineOriginalEmojiProductID, "164"),
	"\u2764":     lineSticonURL(lineOriginalEmojiProductID, "165"),
	"\U0001F494": lineSticonURL(lineOriginalEmojiProductID, "166"),
	"\U0001F495": lineSticonURL(lineTrialEmojiProductID, "224"),
	"\U0001F496": lineSticonURL(lineTrialEmojiProductID, "225"),
	"\U0001F497": lineSticonURL(lineTrialEmojiProductID, "226"),
	"\U0001F498": lineSticonURL(lineTrialEmojiProductID, "227"),
	"\U0001F525": lineSticonURL(lineOriginalEmojiProductID, "167"),
	"\u2728":     lineSticonURL(lineOriginalEmojiProductID, "168"),
	"\U0001F4A6": lineSticonURL(lineOriginalEmojiProductID, "169"),
	"\U0001F3B5": lineSticonURL(lineOriginalEmojiProductID, "170"),
	"\U0001F3B6": lineSticonURL(lineOriginalEmojiProductID, "171"),
	"\U0001F389": lineSticonURL(lineOriginalEmojiProductID, "172"),
	"\U0001F34E": lineSticonURL(lineOriginalEmojiProductID, "173"),
	"\U0001F34C": lineSticonURL(lineOriginalEmojiProductID, "174"),
	"\U0001F966": lineSticonURL(lineOriginalEmojiProductID, "175"),
	"\U0001F35E": lineSticonURL(lineOriginalEmojiProductID, "176"),
	"\U0001F356": lineSticonURL(lineOriginalEmojiProductID, "177"),
	"\U0001F354": lineSticonURL(lineOriginalEmojiProductID, "178"),
	"\U0001F366": lineSticonURL(lineOriginalEmojiProductID, "179"),
	"\U0001F382": lineSticonURL(lineOriginalEmojiProductID, "180"),
	"\u2615":     lineSticonURL(lineOriginalEmojiProductID, "181"),
	"\U0001F964": lineSticonURL(lineOriginalEmojiProductID, "182"),
	"\U0001F37A": lineSticonURL(lineOriginalEmojiProductID, "183"),
	"\u2600":     lineSticonURL(lineOriginalEmojiProductID, "184"),
	"\u2B50":     lineSticonURL(lineOriginalEmojiProductID, "185"),
	"\U0001F319": lineSticonURL(lineOriginalEmojiProductID, "186"),
	"\U0001F338": lineSticonURL(lineOriginalEmojiProductID, "187"),
	"\U0001FAB4": lineSticonURL(lineOriginalEmojiProductID, "188"),
	"\U0001F332": lineSticonURL(lineOriginalEmojiProductID, "189"),
	"\U0001F30A": lineSticonURL(lineOriginalEmojiProductID, "190"),
	"\u26F0":     lineSticonURL(lineOriginalEmojiProductID, "191"),
	"\U0001F30D": lineSticonURL(lineOriginalEmojiProductID, "192"),
	"\U0001F697": lineSticonURL(lineOriginalEmojiProductID, "193"),
	"\U0001F691": lineSticonURL(lineOriginalEmojiProductID, "194"),
	"\u26BD":     lineSticonURL(lineOriginalEmojiProductID, "195"),
	"\U0001F3A4": lineSticonURL(lineOriginalEmojiProductID, "196"),
	"\U0001F3B8": lineSticonURL(lineOriginalEmojiProductID, "197"),
	"\U0001F6E0": lineSticonURL(lineOriginalEmojiProductID, "198"),
	"\U0001F552": lineSticonURL(lineOriginalEmojiProductID, "199"),
	"\u2705":     lineSticonURL(lineOriginalEmojiProductID, "200"),
	"\u274C":     lineSticonURL(lineOriginalEmojiProductID, "201"),
	"0":          lineSticonURL(lineOriginalEmojiProductID, "202"),
	"1":          lineSticonURL(lineOriginalEmojiProductID, "203"),
	"2":          lineSticonURL(lineOriginalEmojiProductID, "204"),
	"3":          lineSticonURL(lineOriginalEmojiProductID, "205"),
	"4":          lineSticonURL(lineOriginalEmojiProductID, "206"),
	"5":          lineSticonURL(lineOriginalEmojiProductID, "207"),
	"6":          lineSticonURL(lineOriginalEmojiProductID, "208"),
	"7":          lineSticonURL(lineOriginalEmojiProductID, "209"),
	"8":          lineSticonURL(lineOriginalEmojiProductID, "210"),
	"9":          lineSticonURL(lineOriginalEmojiProductID, "211"),
}

func lineSticonURL(productID, emojiID string) string {
	return fmt.Sprintf("https://stickershop.line-scdn.net/sticonshop/v1/sticon/%s/android/%s.png", productID, emojiID)
}

func parseLineSticonURL(rawURL string) (linePaidReactionRef, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return linePaidReactionRef{}, err
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for i := 0; i+5 < len(parts); i++ {
		if parts[i] != "sticonshop" || parts[i+1] != "v1" || parts[i+2] != "sticon" || parts[i+4] != "android" {
			continue
		}
		productID := parts[i+3]
		emojiFile := parts[i+5]
		emojiID := strings.TrimSuffix(emojiFile, ".png")
		if productID == "" || emojiID == "" || emojiID == emojiFile {
			break
		}
		version := 1
		if rawVersion := parsed.Query().Get("v"); rawVersion != "" {
			version, _ = strconv.Atoi(rawVersion)
		}
		return linePaidReactionRef{
			ProductID:    productID,
			EmojiID:      emojiID,
			ResourceType: 1,
			Version:      version,
		}, nil
	}
	return linePaidReactionRef{}, fmt.Errorf("not a LINE sticon URL: %s", rawURL)
}

func normalizeMatrixReactionKey(key string) string {
	key = strings.Map(func(r rune) rune {
		switch r {
		case '\uFE0E', '\uFE0F':
			return -1
		default:
			return r
		}
	}, key)

	runes := []rune(key)
	if len(runes) == 2 && runes[1] == '\u20E3' && runes[0] >= '0' && runes[0] <= '9' {
		return string(runes[0])
	}
	return key
}

func linePaidReactionForMatrixEmoji(key string) (linePaidReactionRef, bool) {
	rawURL, ok := lineEmojiReactionURLs[normalizeMatrixReactionKey(key)]
	if !ok {
		return linePaidReactionRef{}, false
	}
	ref, err := parseLineSticonURL(rawURL)
	if err != nil {
		return linePaidReactionRef{}, false
	}
	return ref, true
}

func unsupportedMatrixReactionError(key string) error {
	return bridgev2.WrapErrorInStatus(fmt.Errorf("LINE does not support Matrix reaction %q", key)).
		WithStatus(event.MessageStatusFail).
		WithIsCertain(true).
		WithErrorAsMessage().
		WithErrorReason(event.MessageStatusUnsupported)
}

func invalidReactionTargetError(messageID string) error {
	return bridgev2.WrapErrorInStatus(fmt.Errorf("LINE reaction target message ID %q is invalid", messageID)).
		WithStatus(event.MessageStatusFail).
		WithIsCertain(true).
		WithErrorAsMessage().
		WithErrorReason(event.MessageStatusUnsupported)
}

func reactionNotAMemberError() error {
	return bridgev2.WrapErrorInStatus(fmt.Errorf("LINE says this account is not a member of the chat")).
		WithStatus(event.MessageStatusFail).
		WithIsCertain(true).
		WithErrorAsMessage().
		WithErrorReason(event.MessageStatusNoPermission)
}

func parseReactionTargetMessageID(messageID networkid.MessageID) (string, error) {
	raw := string(messageID)
	if raw == "" || strings.HasPrefix(raw, "local-") || strings.HasPrefix(raw, "$") {
		return "", invalidReactionTargetError(raw)
	}
	if _, err := strconv.ParseInt(raw, 10, 64); err != nil {
		return "", invalidReactionTargetError(raw)
	}
	return raw, nil
}

func (lc *LineClient) nextReqSeq() int {
	now := time.Now()

	lc.reqSeqMu.Lock()
	defer lc.reqSeqMu.Unlock()

	lc.cleanupSentReqSeqsLocked(now)
	if lc.sentReqSeqs == nil {
		lc.sentReqSeqs = make(map[int]time.Time)
	}
	if lc.lastReqSeq <= 0 {
		lc.lastReqSeq = int(now.UnixMilli() % maxLineReqSeq)
	}

	for {
		lc.lastReqSeq++
		if lc.lastReqSeq <= 0 || lc.lastReqSeq >= maxLineReqSeq {
			lc.lastReqSeq = 1
		}
		if _, exists := lc.sentReqSeqs[lc.lastReqSeq]; !exists {
			lc.sentReqSeqs[lc.lastReqSeq] = now
			return lc.lastReqSeq
		}
	}
}

func (lc *LineClient) cleanupSentReqSeqsLocked(now time.Time) {
	for reqSeq, sentAt := range lc.sentReqSeqs {
		if now.Sub(sentAt) > sentReqSeqTTL {
			delete(lc.sentReqSeqs, reqSeq)
		}
	}
}

func (lc *LineClient) trackReqSeq(reqSeq int) {
	if reqSeq <= 0 {
		return
	}
	now := time.Now()

	lc.reqSeqMu.Lock()
	if lc.sentReqSeqs == nil {
		lc.sentReqSeqs = make(map[int]time.Time)
	}
	lc.cleanupSentReqSeqsLocked(now)
	lc.sentReqSeqs[reqSeq] = now
	if reqSeq > lc.lastReqSeq {
		lc.lastReqSeq = reqSeq
	}
	lc.reqSeqMu.Unlock()
}

func (lc *LineClient) consumeSentReqSeq(reqSeq int) bool {
	if reqSeq <= 0 {
		return false
	}
	now := time.Now()

	lc.reqSeqMu.Lock()
	lc.cleanupSentReqSeqsLocked(now)
	_, ok := lc.sentReqSeqs[reqSeq]
	if ok {
		delete(lc.sentReqSeqs, reqSeq)
	}
	lc.reqSeqMu.Unlock()
	return ok
}

func (lc *LineClient) PreHandleMatrixReaction(ctx context.Context, msg *bridgev2.MatrixReaction) (bridgev2.MatrixReactionPreResponse, error) {
	key := msg.Content.RelatesTo.GetAnnotationKey()
	ref, ok := linePaidReactionForMatrixEmoji(key)
	if !ok {
		return bridgev2.MatrixReactionPreResponse{}, unsupportedMatrixReactionError(key)
	}
	return bridgev2.MatrixReactionPreResponse{
		SenderID:     makeUserID(string(lc.UserLogin.ID)),
		EmojiID:      ref.networkEmojiID(),
		Emoji:        key,
		MaxReactions: 1,
	}, nil
}

func (lc *LineClient) HandleMatrixReaction(ctx context.Context, msg *bridgev2.MatrixReaction) (*database.Reaction, error) {
	key := msg.Content.RelatesTo.GetAnnotationKey()
	ref, ok := linePaidReactionForMatrixEmoji(key)
	if !ok {
		return nil, unsupportedMatrixReactionError(key)
	}
	targetID, err := parseReactionTargetMessageID(msg.TargetMessage.ID)
	if err != nil {
		return nil, err
	}

	client := line.NewClient(lc.AccessToken)
	reqSeq := lc.nextReqSeq()
	if err = client.React(int64(reqSeq), targetID, ref.reactionType()); err != nil {
		if line.IsInvalidPaidReactionType(err) {
			return nil, unsupportedMatrixReactionError(key)
		}
		if line.IsNotAMemberError(err) {
			return nil, reactionNotAMemberError()
		}
		return nil, err
	}

	return &database.Reaction{
		EmojiID: ref.networkEmojiID(),
		Emoji:   key,
	}, nil
}

func (lc *LineClient) HandleMatrixReactionRemove(ctx context.Context, msg *bridgev2.MatrixReactionRemove) error {
	if msg.TargetReaction == nil {
		return errors.New("target reaction is missing")
	}
	targetID, err := parseReactionTargetMessageID(msg.TargetReaction.MessageID)
	if err != nil {
		return err
	}
	client := line.NewClient(lc.AccessToken)
	reqSeq := lc.nextReqSeq()
	err = client.CancelReaction(int64(reqSeq), targetID)
	if line.IsNotAMemberError(err) {
		return reactionNotAMemberError()
	}
	return err
}
