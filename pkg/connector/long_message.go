package connector

import (
	"context"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

const (
	maxMatrixTextBytes     = 40000
	maxFallbackBodyBytes   = 500
	longFormattedBodyBytes = 20000
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
		before := textContentSize(part.Content)
		if !cutOffLongContent(part.Content, isGhost) {
			continue
		}
		zerolog.Ctx(ctx).Warn().
			Int("content_type", int(contentType)).
			Int("part_index", i).
			Int("size_before", before).
			Int("size_after", textContentSize(part.Content)).
			Msg("Cut off long LINE message to fit in a Matrix event")
	}
}

func textContentSize(content *event.MessageEventContent) int {
	size := len(content.Body) + len(content.FormattedBody)
	if content.Mentions != nil {
		for _, userID := range content.Mentions.UserIDs {
			size += len(userID)
		}
	}
	return size
}

func cutOffLongContent(content *event.MessageEventContent, isGhost func(id.UserID) bool) bool {
	if textContentSize(content) <= maxMatrixTextBytes {
		return false
	}
	if content.Mentions != nil && isGhost != nil {
		content.Mentions.UserIDs = slices.DeleteFunc(content.Mentions.UserIDs, isGhost)
	}
	if content.FormattedBody == "" {
		if len(content.Body) > maxMatrixTextBytes {
			content.Body = truncateUTF8(content.Body, maxMatrixTextBytes) + longMessageCutOffText
		}
	} else if len(content.FormattedBody) > longFormattedBodyBytes && len(content.Body) > maxFallbackBodyBytes {
		content.Body = truncateUTF8(content.Body, maxFallbackBodyBytes) + "[...]"
	}
	if len(content.FormattedBody) > maxMatrixTextBytes {
		content.FormattedBody = truncateHTML(content.FormattedBody, maxMatrixTextBytes) + longMessageCutOffHTML
	}
	return true
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
