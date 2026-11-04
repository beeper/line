package connector

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

const (
	maxEncodedContentBytes = 40000
	maxFallbackBodyBytes   = 500
	longMessageCutOffText  = "\n\n[long message cut off]"
	longMessageCutOffHTML  = "<br><em>[long message cut off]</em>"
)

func (lc *LineClient) cutOffLongMessage(ctx context.Context, converted *bridgev2.ConvertedMessage, contentType ContentType) {
	if converted == nil {
		return
	}
	var isGhost func(id.UserID) bool
	if lc.UserLogin != nil && lc.UserLogin.Bridge != nil {
		isGhost = lc.UserLogin.Bridge.IsGhostMXID
	}
	for i, part := range converted.Parts {
		if part == nil || part.Content == nil {
			continue
		}
		before := encodedContentSize(part.Content)
		if before <= maxEncodedContentBytes {
			continue
		}
		cutOffLongContent(part.Content, isGhost)
		zerolog.Ctx(ctx).Warn().
			Int("content_type", int(contentType)).
			Int("part_index", i).
			Int("size_before", before).
			Int("size_after", encodedContentSize(part.Content)).
			Msg("Cut off long LINE message to fit in a Matrix event")
	}
}

func encodedContentSize(content *event.MessageEventContent) int {
	data, err := json.Marshal(content)
	if err != nil {
		return len(content.Body) + len(content.FormattedBody)
	}
	return len(data)
}

func cutOffLongContent(content *event.MessageEventContent, isGhost func(id.UserID) bool) {
	if content.Mentions != nil && isGhost != nil {
		content.Mentions.UserIDs = slices.DeleteFunc(content.Mentions.UserIDs, isGhost)
		if encodedContentSize(content) <= maxEncodedContentBytes {
			return
		}
	}
	if content.FormattedBody != "" && len(content.Body) > maxFallbackBodyBytes {
		content.Body = truncateUTF8(content.Body, maxFallbackBodyBytes) + "[...]"
		if encodedContentSize(content) <= maxEncodedContentBytes {
			return
		}
	}
	if content.FormattedBody != "" {
		shrinkToFit(content, &content.FormattedBody, longMessageCutOffHTML, truncateHTML)
		if encodedContentSize(content) <= maxEncodedContentBytes {
			return
		}
	}
	shrinkToFit(content, &content.Body, longMessageCutOffText, truncateUTF8)
}

func shrinkToFit(content *event.MessageEventContent, field *string, marker string, truncate func(string, int) string) {
	original := *field
	fits := func(n int) bool {
		*field = truncate(original, n) + marker
		return encodedContentSize(content) <= maxEncodedContentBytes
	}
	lo, hi := 0, len(original)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if fits(mid) {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	*field = truncate(original, lo) + marker
}

func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	for maxBytes > 0 && !utf8.RuneStart(s[maxBytes]) {
		maxBytes--
	}
	return s[:maxBytes]
}

func truncateHTML(s string, maxBytes int) string {
	s = truncateUTF8(s, maxBytes)
	if idx := strings.LastIndexByte(s, '<'); idx > strings.LastIndexByte(s, '>') {
		s = s[:idx]
	}
	if idx := strings.LastIndexByte(s, '&'); idx > strings.LastIndexByte(s, ';') {
		s = s[:idx]
	}
	return s
}
