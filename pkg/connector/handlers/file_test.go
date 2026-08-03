package handlers

import (
	"testing"

	"maunium.net/go/mautrix/event"
)

func TestOversizedFileNotice(t *testing.T) {
	relatesTo := &event.RelatesTo{}
	converted := oversizedFileNotice(BeeperMaxFileSize+1, relatesTo)
	if converted == nil || len(converted.Parts) != 1 {
		t.Fatalf("converted = %#v, want one notice part", converted)
	}

	part := converted.Parts[0]
	if part.Type != event.EventMessage {
		t.Fatalf("event type = %v, want %v", part.Type, event.EventMessage)
	}
	if part.Content == nil {
		t.Fatal("notice content is nil")
	}
	if part.Content.MsgType != event.MsgNotice {
		t.Fatalf("message type = %v, want %v", part.Content.MsgType, event.MsgNotice)
	}
	if part.Content.Body != oversizedFileBody {
		t.Fatalf("body = %q, want %q", part.Content.Body, oversizedFileBody)
	}
	if part.Content.RelatesTo != relatesTo {
		t.Fatalf("relates_to = %#v, want original pointer %#v", part.Content.RelatesTo, relatesTo)
	}
}

func TestOversizedFileNoticeAllowsLimitAndBelow(t *testing.T) {
	for _, size := range []int{0, BeeperMaxFileSize - 1, BeeperMaxFileSize} {
		if converted := oversizedFileNotice(size, nil); converted != nil {
			t.Fatalf("size %d converted = %#v, want nil", size, converted)
		}
	}
}
