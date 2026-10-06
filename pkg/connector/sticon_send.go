package connector

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"

	"github.com/highesttt/matrix-line-messenger/pkg/connector/handlers"
	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

type sticonReplacement struct {
	marker string
	emote  lineSticker
	alt    string
}

func utf16Length(text string) int {
	length, _ := byteIndexToUTF16Offset(text, len(text))
	return length
}

func convertOutgoingSticons(
	ctx context.Context,
	content *event.MessageEventContent,
	resolve func(id.ContentURIString) (*lineSticker, error),
) (string, map[string]string, error) {
	if content.Format != event.FormatHTML || !strings.Contains(content.FormattedBody, "data-mx-emoticon") {
		return content.Body, nil, nil
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", nil, err
	}
	markerPrefix := "LINESTICON" + hex.EncodeToString(nonce)
	var replacements []sticonReplacement
	var resolveErr error
	resolved := make(map[id.ContentURIString]*lineSticker)
	parser := *format.TextHTMLParser
	parser.ImageConverter = func(src, alt, _, _, _ string, isEmoji bool) string {
		if !isEmoji || resolveErr != nil {
			return alt
		}
		if len(replacements) >= maxEmotesPerMessage {
			resolveErr = fmt.Errorf("LINE supports at most %d inline emoji per message", maxEmotesPerMessage)
			return alt
		}
		mxc := id.ContentURIString(src)
		emote, ok := resolved[mxc]
		if !ok {
			emote, resolveErr = resolve(mxc)
			if resolveErr != nil {
				return alt
			}
			resolved[mxc] = emote
		}
		if emote == nil || emote.Shop != line.SticonShop {
			return alt
		}
		if alt == "" {
			alt = emote.Body
		}
		marker := fmt.Sprintf("%s_%d_END", markerPrefix, len(replacements))
		replacements = append(replacements, sticonReplacement{marker: marker, emote: *emote, alt: alt})
		return marker
	}
	text := parser.Parse(content.FormattedBody, format.NewContext(ctx))
	if resolveErr != nil {
		return "", nil, resolveErr
	}
	if len(replacements) == 0 {
		return content.Body, nil, nil
	}

	var body strings.Builder
	resources := make([]handlers.SticonResource, 0, len(replacements))
	var ownership []string
	offset := 0
	for _, replacement := range replacements {
		before, after, found := strings.Cut(text, replacement.marker)
		if !found {
			return "", nil, fmt.Errorf("could not locate inline emoji")
		}
		version, err := strconv.Atoi(replacement.emote.Version)
		if err != nil {
			return "", nil, fmt.Errorf("invalid LINE emoji version: %w", err)
		}
		resourceType := "STATIC"
		if replacement.emote.Option == "A" {
			resourceType = "ANIMATION"
		}
		body.WriteString(before)
		offset += utf16Length(before)
		end := offset + utf16Length(replacement.alt)
		resources = append(resources, handlers.SticonResource{
			Start:        offset,
			End:          end,
			ProductID:    replacement.emote.ProductID,
			SticonID:     replacement.emote.ID,
			Version:      version,
			ResourceType: resourceType,
		})
		body.WriteString(replacement.alt)
		offset = end
		text = after
		if !slices.Contains(ownership, replacement.emote.ProductID) {
			ownership = append(ownership, replacement.emote.ProductID)
		}
	}
	body.WriteString(text)
	if offset+utf16Length(text) > maxTextLength {
		return "", nil, fmt.Errorf("LINE text exceeds %d UTF-16 units", maxTextLength)
	}
	replace, err := json.Marshal(map[string]any{"sticon": map[string]any{"resources": resources}})
	if err != nil {
		return "", nil, err
	}
	ownershipJSON, err := json.Marshal(ownership)
	if err != nil {
		return "", nil, err
	}
	return body.String(), map[string]string{
		"REPLACE":          string(replace),
		"STICON_OWNERSHIP": string(ownershipJSON),
	}, nil
}

func lineTextPayload(body string, metadata map[string]string) ([]byte, error) {
	payload := map[string]any{"text": body}
	if replace := metadata["REPLACE"]; replace != "" {
		payload["REPLACE"] = json.RawMessage(replace)
	}
	return json.Marshal(payload)
}
