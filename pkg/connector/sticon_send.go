package connector

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"

	"github.com/beeper/line/pkg/connector/handlers"
)

type textMention struct {
	userID     id.UserID
	mid        string
	start, end int
}

func outgoingText(ctx context.Context, content *event.MessageEventContent, resolve func(id.ContentURIString) *lineSticker, mentionMID func(id.UserID) (string, bool)) (string, map[string]string, []textMention) {
	mentioned := make(map[id.UserID]bool)
	if content.Mentions != nil {
		for _, userID := range content.Mentions.UserIDs {
			mentioned[userID] = true
		}
	}
	if content.Format != event.FormatHTML || (len(mentioned) == 0 && !strings.Contains(content.FormattedBody, "data-mx-emoticon")) {
		return content.Body, nil, nil
	}
	type token struct {
		text    string
		sticon  *lineSticker
		mention textMention
	}
	var tokens []token
	resolved := make(map[id.ContentURIString]*lineSticker)
	parser := *format.TextHTMLParser
	parser.ImageConverter = func(src, alt, _, _, _ string, isEmoji bool) string {
		if isEmoji {
			mxc := id.ContentURIString(src)
			sticon, ok := resolved[mxc]
			if !ok {
				sticon = resolve(mxc)
				resolved[mxc] = sticon
			}
			if sticon != nil {
				tokens = append(tokens, token{text: sticon.Body, sticon: sticon})
				return "\x00"
			}
		}
		return alt
	}
	parser.PillConverter = func(displayname, mxid, eventID string, pctx format.Context) string {
		userID := id.UserID(mxid)
		if eventID == "" && mentioned[userID] && strings.TrimSpace(displayname) != "" {
			if mid, ok := mentionMID(userID); ok {
				if !strings.HasPrefix(displayname, "@") {
					displayname = "@" + displayname
				}
				tokens = append(tokens, token{text: displayname, mention: textMention{userID: userID, mid: mid}})
				return "\x00"
			}
		}
		return format.DefaultPillConverter(displayname, mxid, eventID, pctx)
	}
	parts := strings.Split(parser.Parse(content.FormattedBody, format.NewContext(ctx)), "\x00")
	if len(tokens) == 0 || len(parts) != len(tokens)+1 {
		return content.Body, nil, nil
	}
	var body strings.Builder
	var offset int
	var ownership []string
	var resources []handlers.SticonResource
	var mentions []textMention
	for i, tok := range tokens {
		body.WriteString(parts[i])
		offset += len(utf16.Encode([]rune(parts[i])))
		start := body.Len()
		body.WriteString(tok.text)
		end := offset + len(utf16.Encode([]rune(tok.text)))
		if tok.sticon == nil {
			tok.mention.start, tok.mention.end = start, body.Len()
			mentions = append(mentions, tok.mention)
			offset = end
			continue
		}
		version, _ := strconv.Atoi(tok.sticon.Version)
		resource := handlers.SticonResource{
			Start:        offset,
			End:          end,
			ProductID:    tok.sticon.ProductID,
			SticonID:     tok.sticon.ID,
			Version:      version,
			ResourceType: "STATIC",
		}
		if tok.sticon.Option == "A" {
			resource.ResourceType = "ANIMATION"
		}
		resources = append(resources, resource)
		offset = end
		if !slices.Contains(ownership, tok.sticon.ProductID) {
			ownership = append(ownership, tok.sticon.ProductID)
		}
	}
	body.WriteString(parts[len(tokens)])
	if len(resources) == 0 {
		return body.String(), nil, mentions
	}
	var replace struct {
		Sticon struct {
			Resources []handlers.SticonResource `json:"resources"`
		} `json:"sticon"`
	}
	replace.Sticon.Resources = resources
	replaceJSON, _ := json.Marshal(replace)
	ownershipJSON, _ := json.Marshal(ownership)
	return body.String(), map[string]string{"REPLACE": string(replaceJSON), "STICON_OWNERSHIP": string(ownershipJSON)}, mentions
}
