package connector

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

func TestCapabilitiesAdvertiseSupportedReactions(t *testing.T) {
	infoVersion, capabilityVersion := (&LineConnector{}).GetBridgeInfoVersion()
	if infoVersion != 1 || capabilityVersion != 2 {
		t.Fatalf("bridge info/capability versions = %d/%d, want 1/2", infoVersion, capabilityVersion)
	}

	caps := (&LineClient{}).GetCapabilities(context.Background(), nil)
	if caps.Reaction != event.CapLevelPartialSupport {
		t.Fatalf("Reaction = %d, want partial support", caps.Reaction)
	}
	if caps.ReactionCount != 1 {
		t.Fatalf("ReactionCount = %d, want 1", caps.ReactionCount)
	}
	if len(caps.AllowedReactions) != len(lineEmojiReactionURLs) {
		t.Fatalf("AllowedReactions has %d entries, want %d", len(caps.AllowedReactions), len(lineEmojiReactionURLs))
	}
	if !slices.IsSorted(caps.AllowedReactions) {
		t.Fatal("AllowedReactions must be sorted so room capability IDs are stable")
	}

	seen := make(map[string]struct{}, len(caps.AllowedReactions))
	for _, reaction := range caps.AllowedReactions {
		if _, duplicate := seen[reaction]; duplicate {
			t.Fatalf("AllowedReactions contains duplicate %q", reaction)
		}
		seen[reaction] = struct{}{}
		if _, ok := linePaidReactionForMatrixEmoji(reaction); !ok {
			t.Fatalf("advertised reaction %q is not accepted by the LINE reaction mapper", reaction)
		}
	}

	for _, supported := range []string{"\U0001F44D\uFE0F", "9\uFE0F\u20E3"} {
		if !slices.Contains(caps.AllowedReactions, supported) {
			t.Fatalf("supported reaction %q is not advertised", supported)
		}
	}
	if slices.Contains(caps.AllowedReactions, "\U0001F625") {
		t.Fatal("unsupported reaction 😥 must not be advertised")
	}

	caps.AllowedReactions[0] = "mutated"
	freshCaps := (&LineClient{}).GetCapabilities(context.Background(), nil)
	if freshCaps.AllowedReactions[0] == "mutated" {
		t.Fatal("GetCapabilities returned a shared mutable reaction list")
	}
}

func TestNormalizeMatrixReactionKey(t *testing.T) {
	tests := map[string]string{
		"9\uFE0F\u20E3": "9",
		"9\u20E3":       "9",
		"\u2764\uFE0F":  "\u2764",
		"\u2600\uFE0F":  "\u2600",
		"\u2615":        "\u2615",
	}

	for input, expected := range tests {
		if actual := normalizeMatrixReactionKey(input); actual != expected {
			t.Fatalf("normalizeMatrixReactionKey(%q) = %q, want %q", input, actual, expected)
		}
	}
}

func TestNextReqSeqIsUniqueAndNonZeroAcrossWrap(t *testing.T) {
	lc := &LineClient{
		lastReqSeq: maxLineReqSeq - 1,
	}

	first := lc.nextReqSeq()
	second := lc.nextReqSeq()

	if first <= 0 || second <= 0 {
		t.Fatalf("reqSeqs = %d/%d, want non-zero positive values", first, second)
	}
	if first >= maxLineReqSeq || second >= maxLineReqSeq {
		t.Fatalf("reqSeqs = %d/%d, want values below %d", first, second, maxLineReqSeq)
	}
	if first == second {
		t.Fatalf("reqSeqs both = %d, want unique values", first)
	}
	if _, ok := lc.sentReqSeqs[first]; !ok {
		t.Fatalf("first reqSeq %d was not tracked", first)
	}
	if _, ok := lc.sentReqSeqs[second]; !ok {
		t.Fatalf("second reqSeq %d was not tracked", second)
	}
}

func TestNextUntrackedReqSeqDoesNotLookLikeASentReaction(t *testing.T) {
	lc := &LineClient{}

	untracked := lc.nextUntrackedReqSeq()
	tracked := lc.nextReqSeq()

	if untracked <= 0 || tracked <= 0 || untracked == tracked {
		t.Fatalf("request sequences = %d/%d, want distinct positive values", untracked, tracked)
	}
	if lc.consumeSentReqSeq(untracked) {
		t.Fatalf("untracked request sequence %d was recorded as sent", untracked)
	}
	if !lc.consumeSentReqSeq(tracked) {
		t.Fatalf("tracked request sequence %d was not recorded", tracked)
	}
}

func TestTrackReqSeqCleansExpiredEntries(t *testing.T) {
	now := time.Now()
	lc := &LineClient{
		sentReqSeqs: map[int]time.Time{
			1: now.Add(-sentReqSeqTTL - time.Second),
			2: now,
		},
	}

	lc.trackReqSeq(3)

	if _, ok := lc.sentReqSeqs[1]; ok {
		t.Fatal("expired reqSeq was not cleaned")
	}
	if _, ok := lc.sentReqSeqs[2]; !ok {
		t.Fatal("fresh reqSeq was unexpectedly removed")
	}
	if _, ok := lc.sentReqSeqs[3]; !ok {
		t.Fatal("new reqSeq was not tracked")
	}
}

func TestParseLineSticonURL(t *testing.T) {
	ref, err := parseLineSticonURL("https://stickershop.line-scdn.net/sticonshop/v1/sticon/670e0cce840a8236ddd4ee4c/android/211.png?385d6a34-b435-4fd8-8428-6f73eef5f110")
	if err != nil {
		t.Fatal(err)
	}
	if ref.ProductID != lineOriginalEmojiProductID {
		t.Fatalf("ProductID = %q, want %q", ref.ProductID, lineOriginalEmojiProductID)
	}
	if ref.EmojiID != "211" {
		t.Fatalf("EmojiID = %q, want 211", ref.EmojiID)
	}
	if ref.ResourceType != 1 || ref.Version != 1 {
		t.Fatalf("ResourceType/Version = %d/%d, want 1/1", ref.ResourceType, ref.Version)
	}

	ref, err = parseLineSticonURL("https://stickershop.line-scdn.net/sticonshop/v1/sticon/670e0cce840a8236ddd4ee4c/android/211.png?v=2")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Version != 2 {
		t.Fatalf("Version = %d, want 2", ref.Version)
	}
}

func TestLinePaidReactionForMatrixEmoji(t *testing.T) {
	ref, ok := linePaidReactionForMatrixEmoji("9\uFE0F\u20E3")
	if !ok {
		t.Fatal("expected keycap nine to be supported")
	}
	if ref.ProductID != lineOriginalEmojiProductID || ref.EmojiID != "211" {
		t.Fatalf("reaction ref = %#v, want product %q emoji 211", ref, lineOriginalEmojiProductID)
	}

	ref, ok = linePaidReactionForMatrixEmoji("\u2764\uFE0F")
	if !ok {
		t.Fatal("expected variation-selector heart to be supported")
	}
	if ref.EmojiID != "165" {
		t.Fatalf("heart EmojiID = %q, want 165", ref.EmojiID)
	}

	ref, ok = linePaidReactionForMatrixEmoji("\U0001F495")
	if !ok {
		t.Fatal("expected two hearts to be supported")
	}
	if ref.ProductID != lineTrialEmojiProductID || ref.EmojiID != "224" {
		t.Fatalf("two hearts ref = %#v, want product %q emoji 224", ref, lineTrialEmojiProductID)
	}

	ref, ok = linePaidReactionForMatrixEmoji("\U0001F496")
	if !ok {
		t.Fatal("expected sparkling heart to be supported")
	}
	if ref.ProductID != lineTrialEmojiProductID || ref.EmojiID != "225" {
		t.Fatalf("sparkling heart ref = %#v, want product %q emoji 225", ref, lineTrialEmojiProductID)
	}

	ref, ok = linePaidReactionForMatrixEmoji("\U0001F497")
	if !ok {
		t.Fatal("expected growing heart to be supported")
	}
	if ref.ProductID != lineTrialEmojiProductID || ref.EmojiID != "226" {
		t.Fatalf("growing heart ref = %#v, want product %q emoji 226", ref, lineTrialEmojiProductID)
	}

	ref, ok = linePaidReactionForMatrixEmoji("\U0001F498")
	if !ok {
		t.Fatal("expected heart with arrow to be supported")
	}
	if ref.ProductID != lineTrialEmojiProductID || ref.EmojiID != "227" {
		t.Fatalf("heart with arrow ref = %#v, want product %q emoji 227", ref, lineTrialEmojiProductID)
	}

	ref, ok = linePaidReactionForMatrixEmoji("\U0001F642")
	if !ok {
		t.Fatal("expected slightly smiling face to be supported")
	}
	if ref.EmojiID != "077" {
		t.Fatalf("smile EmojiID = %q, want 077", ref.EmojiID)
	}

	ref, ok = linePaidReactionForMatrixEmoji("\U0001F979")
	if !ok {
		t.Fatal("expected face holding back tears to be supported")
	}
	if ref.EmojiID != "081" {
		t.Fatalf("face holding back tears EmojiID = %q, want 081", ref.EmojiID)
	}

	ref, ok = linePaidReactionForMatrixEmoji("\U0001F60D")
	if !ok {
		t.Fatal("expected smiling face with heart-eyes to be supported")
	}
	if ref.ProductID != lineTrialEmojiProductID || ref.EmojiID != "001" {
		t.Fatalf("smiling face with heart-eyes ref = %#v, want product %q emoji 001", ref, lineTrialEmojiProductID)
	}

	ref, ok = linePaidReactionForMatrixEmoji("\U0001F606")
	if !ok {
		t.Fatal("expected grinning squinting face to be supported")
	}
	if ref.ProductID != lineTrialEmojiProductID || ref.EmojiID != "002" {
		t.Fatalf("grinning squinting face ref = %#v, want product %q emoji 002", ref, lineTrialEmojiProductID)
	}

	ref, ok = linePaidReactionForMatrixEmoji("\U0001F609")
	if !ok {
		t.Fatal("expected winking face to be supported")
	}
	if ref.ProductID != lineTrialEmojiProductID || ref.EmojiID != "011" {
		t.Fatalf("winking face ref = %#v, want product %q emoji 011", ref, lineTrialEmojiProductID)
	}

	ref, ok = linePaidReactionForMatrixEmoji("\U0001F60C")
	if !ok {
		t.Fatal("expected relieved face to be supported")
	}
	if ref.ProductID != lineTrialEmojiProductID || ref.EmojiID != "012" {
		t.Fatalf("relieved face ref = %#v, want product %q emoji 012", ref, lineTrialEmojiProductID)
	}

	ref, ok = linePaidReactionForMatrixEmoji("\U0001F632")
	if !ok {
		t.Fatal("expected astonished face to be supported")
	}
	if ref.ProductID != lineTrialEmojiProductID || ref.EmojiID != "029" {
		t.Fatalf("astonished face ref = %#v, want product %q emoji 029", ref, lineTrialEmojiProductID)
	}

	ref, ok = linePaidReactionForMatrixEmoji("\U0001F611")
	if !ok {
		t.Fatal("expected expressionless face to be supported")
	}
	if ref.ProductID != lineTrialEmojiProductID || ref.EmojiID != "036" {
		t.Fatalf("expressionless face ref = %#v, want product %q emoji 036", ref, lineTrialEmojiProductID)
	}

	ref, ok = linePaidReactionForMatrixEmoji("\U0001F616")
	if !ok {
		t.Fatal("expected confounded face to be supported")
	}
	if ref.ProductID != lineTrialEmojiProductID || ref.EmojiID != "129" {
		t.Fatalf("confounded face ref = %#v, want product %q emoji 129", ref, lineTrialEmojiProductID)
	}

	ref, ok = linePaidReactionForMatrixEmoji("\U0001F624")
	if !ok {
		t.Fatal("expected face with steam from nose to be supported")
	}
	if ref.ProductID != lineTrialEmojiProductID || ref.EmojiID != "135" {
		t.Fatalf("face with steam from nose ref = %#v, want product %q emoji 135", ref, lineTrialEmojiProductID)
	}

	ref, ok = linePaidReactionForMatrixEmoji("\U0001F612")
	if !ok {
		t.Fatal("expected unamused face to be supported")
	}
	if ref.ProductID != lineTrialEmojiProductID || ref.EmojiID != "141" {
		t.Fatalf("unamused face ref = %#v, want product %q emoji 141", ref, lineTrialEmojiProductID)
	}

	ref, ok = linePaidReactionForMatrixEmoji("\U0001FAE8")
	if !ok {
		t.Fatal("expected shaking face to be supported")
	}
	if ref.ProductID != lineTrialEmojiProductID || ref.EmojiID != "142" {
		t.Fatalf("shaking face ref = %#v, want product %q emoji 142", ref, lineTrialEmojiProductID)
	}

	ref, ok = linePaidReactionForMatrixEmoji("\U0001F978")
	if !ok {
		t.Fatal("expected disguised face to be supported")
	}
	if ref.ProductID != lineTrialEmojiProductID || ref.EmojiID != "146" {
		t.Fatalf("disguised face ref = %#v, want product %q emoji 146", ref, lineTrialEmojiProductID)
	}

	ref, ok = linePaidReactionForMatrixEmoji("\U0001F442")
	if !ok {
		t.Fatal("expected ear to be supported")
	}
	if ref.ProductID != lineTrialEmojiProductID || ref.EmojiID != "246" {
		t.Fatalf("ear ref = %#v, want product %q emoji 246", ref, lineTrialEmojiProductID)
	}

	ref, ok = linePaidReactionForMatrixEmoji("\U0001F443")
	if !ok {
		t.Fatal("expected nose to be supported")
	}
	if ref.ProductID != lineTrialEmojiProductID || ref.EmojiID != "245" {
		t.Fatalf("nose ref = %#v, want product %q emoji 245", ref, lineTrialEmojiProductID)
	}

	ref, ok = linePaidReactionForMatrixEmoji("\U0001F444")
	if !ok {
		t.Fatal("expected mouth to be supported")
	}
	if ref.ProductID != lineTrialEmojiProductID || ref.EmojiID != "247" {
		t.Fatalf("mouth ref = %#v, want product %q emoji 247", ref, lineTrialEmojiProductID)
	}

	ref, ok = linePaidReactionForMatrixEmoji("\U0001F97A")
	if !ok {
		t.Fatal("expected pleading face to be supported")
	}
	if ref.EmojiID != "087" {
		t.Fatalf("pleading face EmojiID = %q, want 087", ref.EmojiID)
	}

	ref, ok = linePaidReactionForMatrixEmoji("\U0001F641")
	if !ok {
		t.Fatal("expected slightly frowning face to be supported")
	}
	if ref.EmojiID != "088" {
		t.Fatalf("slightly frowning face EmojiID = %q, want 088", ref.EmojiID)
	}

	ref, ok = linePaidReactionForMatrixEmoji("\U0001F62E")
	if !ok {
		t.Fatal("expected face with open mouth to be supported")
	}
	if ref.EmojiID != "089" {
		t.Fatalf("face with open mouth EmojiID = %q, want 089", ref.EmojiID)
	}

	ref, ok = linePaidReactionForMatrixEmoji("\U0001F627")
	if !ok {
		t.Fatal("expected anguished face to be supported")
	}
	if ref.EmojiID != "090" {
		t.Fatalf("anguished face EmojiID = %q, want 090", ref.EmojiID)
	}

	ref, ok = linePaidReactionForMatrixEmoji("\U0001F622")
	if !ok {
		t.Fatal("expected crying face to be supported")
	}
	if ref.EmojiID != "092" {
		t.Fatalf("crying face EmojiID = %q, want 092", ref.EmojiID)
	}

	ref, ok = linePaidReactionForMatrixEmoji("\U0001F620")
	if !ok {
		t.Fatal("expected angry face to be supported")
	}
	if ref.EmojiID != "094" {
		t.Fatalf("angry face EmojiID = %q, want 094", ref.EmojiID)
	}

	ref, ok = linePaidReactionForMatrixEmoji("\U0001FAE0")
	if !ok {
		t.Fatal("expected melting face to be supported")
	}
	if ref.EmojiID != "125" {
		t.Fatalf("melting face EmojiID = %q, want 125", ref.EmojiID)
	}

	ref, ok = linePaidReactionForMatrixEmoji("\U0001F972")
	if !ok {
		t.Fatal("expected smiling face with tear to be supported")
	}
	if ref.EmojiID != "102" {
		t.Fatalf("smiling face with tear EmojiID = %q, want 102", ref.EmojiID)
	}

	ref, ok = linePaidReactionForMatrixEmoji("\U0001F44D")
	if !ok {
		t.Fatal("expected thumbs up to be supported")
	}
	if ref.EmojiID != "143" {
		t.Fatalf("thumbs up EmojiID = %q, want 143", ref.EmojiID)
	}

	ref, ok = linePaidReactionForMatrixEmoji("\U0001F34E")
	if !ok {
		t.Fatal("expected apple to be supported")
	}
	if ref.EmojiID != "173" {
		t.Fatalf("apple EmojiID = %q, want 173", ref.EmojiID)
	}

	if _, ok = linePaidReactionForMatrixEmoji("\U0001F984"); ok {
		t.Fatal("unicorn should not be supported in pack-only mapping")
	}
}

func TestParseReactionTargetMessageID(t *testing.T) {
	messageID, err := parseReactionTargetMessageID(networkid.MessageID("616934195205767730"))
	if err != nil {
		t.Fatal(err)
	}
	if messageID != "616934195205767730" {
		t.Fatalf("messageID = %q, want 616934195205767730", messageID)
	}

	for _, bad := range []networkid.MessageID{"", "local-123", "$event:example.org", "not-a-number"} {
		if _, err = parseReactionTargetMessageID(bad); err == nil {
			t.Fatalf("expected %q to be rejected", bad)
		}
	}
}

func TestUnsupportedMatrixReactionError(t *testing.T) {
	err := unsupportedMatrixReactionError("\U0001F44D")
	var status bridgev2.MessageStatus
	if !errors.As(err, &status) {
		t.Fatalf("error %T does not wrap bridgev2.MessageStatus", err)
	}
	if status.ErrorReason != event.MessageStatusUnsupported {
		t.Fatalf("ErrorReason = %q, want %q", status.ErrorReason, event.MessageStatusUnsupported)
	}
	if status.Status != event.MessageStatusFail {
		t.Fatalf("Status = %q, want %q", status.Status, event.MessageStatusFail)
	}
	if !status.IsCertain || !status.ErrorAsMessage {
		t.Fatalf("status certainty/message flags = %v/%v, want true/true", status.IsCertain, status.ErrorAsMessage)
	}
}

func TestReactionNotAMemberError(t *testing.T) {
	err := reactionNotAMemberError()
	var status bridgev2.MessageStatus
	if !errors.As(err, &status) {
		t.Fatalf("error %T does not wrap bridgev2.MessageStatus", err)
	}
	if status.Status != event.MessageStatusFail {
		t.Fatalf("Status = %q, want %q", status.Status, event.MessageStatusFail)
	}
	if status.ErrorReason != event.MessageStatusNoPermission {
		t.Fatalf("ErrorReason = %q, want %q", status.ErrorReason, event.MessageStatusNoPermission)
	}
	if !status.IsCertain || !status.ErrorAsMessage {
		t.Fatalf("status certainty/message flags = %v/%v, want true/true", status.IsCertain, status.ErrorAsMessage)
	}
}

func TestEventSenderForMIDMarksOwnAccount(t *testing.T) {
	lc := &LineClient{
		Mid: "own-mid",
		UserLogin: &bridgev2.UserLogin{
			UserLogin: &database.UserLogin{ID: networkid.UserLoginID("login-mid")},
		},
	}

	for _, tc := range []struct {
		name string
		mid  string
	}{
		// BIOS-36189: an own paid reaction in a DM arrived with the account MID.
		// IsFromMe makes bridgev2 use the source login instead of joining its ghost
		// as a third DM member.
		{name: "BIOS-36189 DM own reaction", mid: "own-mid"},
		{name: "persisted login ID alias", mid: "login-mid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sender := lc.eventSenderForMID(tc.mid)
			if !sender.IsFromMe {
				t.Fatalf("sender %q was not marked IsFromMe", tc.mid)
			}
			if sender.Sender != networkid.UserID(tc.mid) {
				t.Fatalf("sender ID = %q, want %q", sender.Sender, tc.mid)
			}
		})
	}

	other := lc.eventSenderForMID("other-mid")
	if other.IsFromMe {
		t.Fatal("other sender was marked IsFromMe")
	}
	if other.Sender != networkid.UserID("other-mid") {
		t.Fatalf("other sender ID = %q", other.Sender)
	}
}

func TestResolveReactionSenderMID(t *testing.T) {
	lc := &LineClient{
		UserLogin: &bridgev2.UserLogin{
			UserLogin: &database.UserLogin{ID: networkid.UserLoginID("Uself")},
		},
	}

	tests := []struct {
		name    string
		opType  OperationType
		op      line.Operation
		chatMID string
		want    string
	}{
		{
			name:    "type 139 always uses the logged-in user",
			opType:  OpPredefinedReaction,
			op:      line.Operation{Param3: "Uobserver"},
			chatMID: "Cgroup",
			want:    "Uself",
		},
		{
			name:    "type 140 group reaction preserves param3 actor",
			opType:  OpReaction,
			op:      line.Operation{Param3: "Ureactor"},
			chatMID: "Cgroup",
			want:    "Ureactor",
		},
		{
			name:    "type 140 DM can infer a missing actor from chat MID",
			opType:  OpReaction,
			op:      line.Operation{},
			chatMID: "Upeer",
			want:    "Upeer",
		},
		{
			name:    "type 140 DM preserves the param3 actor",
			opType:  OpReaction,
			op:      line.Operation{Param3: "Uactor"},
			chatMID: "Upeer",
			want:    "Uactor",
		},
		{
			name:    "type 140 group cannot use chat MID as actor",
			opType:  OpReaction,
			op:      line.Operation{},
			chatMID: "Cgroup",
			want:    "",
		},
		{
			name:    "type 140 rejects a group MID in param3",
			opType:  OpReaction,
			op:      line.Operation{Param3: "Cgroup"},
			chatMID: "Cgroup",
			want:    "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := lc.resolveReactionSenderMID(tc.opType, tc.op, tc.chatMID); got != tc.want {
				t.Fatalf("resolveReactionSenderMID() = %q, want %q", got, tc.want)
			}
		})
	}
}
