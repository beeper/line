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

func outgoingSticons(ctx context.Context, content *event.MessageEventContent, resolve func(id.ContentURIString) *lineSticker) (string, map[string]string) {
	if content.Format != event.FormatHTML || !strings.Contains(content.FormattedBody, "data-mx-emoticon") {
		return content.Body, nil
	}
	var sticons []*lineSticker
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
				sticons = append(sticons, sticon)
				return "\x00"
			}
		}
		return alt
	}
	parts := strings.Split(parser.Parse(content.FormattedBody, format.NewContext(ctx)), "\x00")
	if len(sticons) == 0 || len(parts) != len(sticons)+1 {
		return content.Body, nil
	}
	var body strings.Builder
	var offset int
	var ownership []string
	resources := make([]handlers.SticonResource, len(sticons))
	for i, sticon := range sticons {
		body.WriteString(parts[i])
		offset += len(utf16.Encode([]rune(parts[i])))
		body.WriteString(sticon.Body)
		version, _ := strconv.Atoi(sticon.Version)
		resources[i] = handlers.SticonResource{
			Start:        offset,
			End:          offset + len(utf16.Encode([]rune(sticon.Body))),
			ProductID:    sticon.ProductID,
			SticonID:     sticon.ID,
			Version:      version,
			ResourceType: "STATIC",
		}
		if sticon.Option == "A" {
			resources[i].ResourceType = "ANIMATION"
		}
		offset = resources[i].End
		if !slices.Contains(ownership, sticon.ProductID) {
			ownership = append(ownership, sticon.ProductID)
		}
	}
	body.WriteString(parts[len(sticons)])
	var replace struct {
		Sticon struct {
			Resources []handlers.SticonResource `json:"resources"`
		} `json:"sticon"`
	}
	replace.Sticon.Resources = resources
	replaceJSON, _ := json.Marshal(replace)
	ownershipJSON, _ := json.Marshal(ownership)
	return body.String(), map[string]string{"REPLACE": string(replaceJSON), "STICON_OWNERSHIP": string(ownershipJSON)}
}
