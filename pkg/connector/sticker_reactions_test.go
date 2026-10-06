package connector

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

func TestImportedLINEEmojiReaction(t *testing.T) {
	lc, _ := stickerTestClient(t)
	pack, _ := emojiTestPack(t, lc)
	key := string(pack.Content.Images["brown_bear_cat"].URL)
	msg := &bridgev2.MatrixReaction{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.ReactionEventContent]{
			Content: &event.ReactionEventContent{
				RelatesTo: event.RelatesTo{Type: event.RelAnnotation, Key: key},
			},
			Portal: &bridgev2.Portal{Portal: &database.Portal{PortalKey: networkid.PortalKey{ID: "ufriend"}}},
		},
		TargetMessage: &database.Message{ID: "123456789"},
	}
	pre, err := lc.PreHandleMatrixReaction(t.Context(), msg)
	if err != nil {
		t.Fatal(err)
	}
	if pre.EmojiID != "paid:emojipack:001" || pre.MaxReactions != 1 {
		t.Fatalf("unexpected pre-handled reaction: %+v", pre)
	}

	previous := newLineAPIClient
	t.Cleanup(func() { newLineAPIClient = previous })
	calls := 0
	newLineAPIClient = func(token string) *line.Client {
		c := line.NewClient(token)
		c.HTTPClient = &http.Client{Transport: stickerTestTransport(func(r *http.Request) (*http.Response, error) {
			if !strings.HasSuffix(r.URL.Path, "/react") {
				t.Fatalf("unexpected request: %s", r.URL)
			}
			var args []line.ReactRequest
			if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
				t.Fatal(err)
			}
			if len(args) != 1 || args[0].MessageID != "123456789" {
				t.Fatalf("unexpected reaction request: %+v", args)
			}
			paid := args[0].ReactionType.PaidReactionType
			if paid == nil || paid.ProductID != "emojipack" || paid.EmojiID != "001" || paid.Version != 2 || paid.ResourceType != 2 {
				t.Fatalf("unexpected paid reaction: %+v", paid)
			}
			calls++
			return stickerResponse(`{"code":0}`), nil
		})}
		return c
	}
	reaction, err := lc.HandleMatrixReaction(t.Context(), msg)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || reaction.Emoji != key || reaction.EmojiID != pre.EmojiID {
		t.Fatalf("reaction=%+v calls=%d", reaction, calls)
	}

	lc.stickerCatalogs[line.SticonShop] = stickerCatalog{Fetched: time.Now()}
	if _, err = lc.resolveMatrixReaction(t.Context(), msg); err == nil {
		t.Fatal("unowned emoji accepted")
	}
	stickerPack, err := lc.DownloadImagePack(t.Context(), "line://stickershop/123")
	if err != nil {
		t.Fatal(err)
	}
	msg.Content.RelatesTo.Key = string(stickerPack.Content.Images["line_100"].URL)
	if _, err = lc.resolveMatrixReaction(t.Context(), msg); err == nil {
		t.Fatal("full-size sticker treated as LINE emoji")
	}
}
