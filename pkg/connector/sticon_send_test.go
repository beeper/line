package connector

import (
	"context"
	"testing"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func TestOutgoingSticons(t *testing.T) {
	sticons := map[id.ContentURIString]*lineSticker{
		"mxc://example.com/moon": {ProductID: "p1", ID: "001", Version: "3", Body: "(moon)", Sticon: true},
		"mxc://example.com/cat":  {ProductID: "p2", ID: "007", Version: "1", Option: "A", Body: "(cat)", Sticon: true},
	}
	calls := 0
	resolve := func(mxc id.ContentURIString) *lineSticker {
		calls++
		return sticons[mxc]
	}
	content := &event.MessageEventContent{
		MsgType: event.MsgText,
		Body:    "😀 :moon: and :cat::moon: :other:",
		Format:  event.FormatHTML,
		FormattedBody: `😀 <img data-mx-emoticon src="mxc://example.com/moon" alt=":moon:"> and ` +
			`<img data-mx-emoticon src="mxc://example.com/cat" alt=":cat:"><img data-mx-emoticon src="mxc://example.com/moon" alt=":moon:"> ` +
			`<img data-mx-emoticon src="mxc://example.com/other" alt=":other:">`,
	}
	body, metadata := outgoingSticons(context.Background(), content, resolve)
	if body != "😀 (moon) and (cat)(moon) :other:" || calls != 3 {
		t.Fatalf("body = %q, resolve calls = %d", body, calls)
	}
	wantReplace := `{"sticon":{"resources":[` +
		`{"S":3,"E":9,"productId":"p1","sticonId":"001","version":3,"resourceType":"STATIC"},` +
		`{"S":14,"E":19,"productId":"p2","sticonId":"007","version":1,"resourceType":"ANIMATION"},` +
		`{"S":19,"E":25,"productId":"p1","sticonId":"001","version":3,"resourceType":"STATIC"}]}}`
	if metadata["REPLACE"] != wantReplace {
		t.Fatalf("REPLACE = %s", metadata["REPLACE"])
	}
	if metadata["STICON_OWNERSHIP"] != `["p1","p2"]` {
		t.Fatalf("STICON_OWNERSHIP = %s", metadata["STICON_OWNERSHIP"])
	}
}

func TestOutgoingSticonsFallsBackToBody(t *testing.T) {
	resolve := func(id.ContentURIString) *lineSticker { return nil }
	for _, content := range []*event.MessageEventContent{
		{Body: "plain"},
		{Body: ":other:", Format: event.FormatHTML, FormattedBody: `<img data-mx-emoticon src="mxc://example.com/other" alt=":other:">`},
	} {
		if body, metadata := outgoingSticons(context.Background(), content, resolve); body != content.Body || metadata != nil {
			t.Fatalf("outgoingSticons(%q) = %q, %v", content.Body, body, metadata)
		}
	}
}
