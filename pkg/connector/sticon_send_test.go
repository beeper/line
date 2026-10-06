package connector

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/highesttt/matrix-line-messenger/pkg/connector/handlers"
	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

func TestOutgoingSticonUTF16AndEncryptedPayload(t *testing.T) {
	emoji := `<img data-mx-emoticon src="mxc://test/cat" alt="(猫)">`
	content := &event.MessageEventContent{
		Body:          "fallback",
		Format:        event.FormatHTML,
		FormattedBody: "<p>🙂 Hello " + emoji + " &amp; " + emoji + "</p>",
	}
	calls := 0
	body, meta, err := convertOutgoingSticons(t.Context(), content, func(id.ContentURIString) (*lineSticker, error) {
		calls++
		return &lineSticker{Shop: line.SticonShop, ID: "001", ProductID: "cats", Version: "2", Option: "A"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if body != "🙂 Hello (猫) & (猫)" || calls != 1 {
		t.Fatalf("%q calls %d", body, calls)
	}
	var replace struct {
		Sticon struct {
			Resources []handlers.SticonResource `json:"resources"`
		} `json:"sticon"`
	}
	if err = json.Unmarshal([]byte(meta["REPLACE"]), &replace); err != nil {
		t.Fatal(err)
	}
	r := replace.Sticon.Resources
	if len(r) != 2 || r[0].Start != 9 || r[0].End != 12 || r[1].Start != 15 || r[1].End != 18 ||
		r[0].ResourceType != "ANIMATION" || r[0].Version != 2 {
		t.Fatalf("%+v", r)
	}
	if meta["STICON_OWNERSHIP"] != `["cats"]` {
		t.Fatal(meta)
	}
	payload, err := lineTextPayload(body, meta)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	json.Unmarshal(payload, &decoded)
	if _, ok := decoded["REPLACE"].(map[string]any); !ok {
		t.Fatalf("REPLACE must be encrypted JSON object: %s", payload)
	}
	if content.Body != "fallback" {
		t.Fatal("mutated input")
	}
}

func TestOutgoingSticonFallbackAndLimits(t *testing.T) {
	content := &event.MessageEventContent{
		Body:          "fallback",
		Format:        event.FormatHTML,
		FormattedBody: `<img src="mxc://test/x" alt="image"> <img data-mx-emoticon src="mxc://test/x" alt="unknown">`,
	}
	body, meta, err := convertOutgoingSticons(t.Context(), content, func(id.ContentURIString) (*lineSticker, error) { return nil, nil })
	if body != "fallback" || meta != nil || err != nil {
		t.Fatalf("%q %v %v", body, meta, err)
	}
	content.FormattedBody = strings.Repeat(`<img data-mx-emoticon src="mxc://test/x" alt="x">`, 251)
	calls := 0
	_, _, err = convertOutgoingSticons(t.Context(), content, func(id.ContentURIString) (*lineSticker, error) {
		calls++
		return &lineSticker{Shop: line.SticonShop, ID: "001", ProductID: "cats", Version: "1"}, nil
	})
	if err == nil || calls != 1 {
		t.Fatalf("limit failed %v calls %d", err, calls)
	}
	expected := errors.New("not owned")
	_, _, err = convertOutgoingSticons(t.Context(), content, func(id.ContentURIString) (*lineSticker, error) { return nil, expected })
	if !errors.Is(err, expected) {
		t.Fatal(err)
	}
}
