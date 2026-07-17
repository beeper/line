package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

// tryUploadEmoji downloads a sticon from the LINE CDN, uploads it to Matrix,
// and returns the MXC URI (or empty on failure).
func (h *Handler) tryUploadEmoji(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, productID, sticonID string) string {
	url := fmt.Sprintf("https://stickershop.line-scdn.net/sticonshop/v1/sticon/%s/android/%s.png", productID, sticonID)
	resp, err := h.HTTPClient.Get(url)
	if err != nil {
		h.Log.Warn().Err(err).Str("product_id", productID).Str("sticon_id", sticonID).Msg("CDN request failed")
		return ""
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		h.Log.Warn().Int("status", resp.StatusCode).Str("product_id", productID).Str("sticon_id", sticonID).Msg("CDN returned non-200")
		return ""
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		h.Log.Warn().Err(err).Str("product_id", productID).Str("sticon_id", sticonID).Msg("Failed to read CDN body")
		return ""
	}

	mimeType := resp.Header.Get("Content-Type")
	if mimeType == "" {
		mimeType = "image/png"
	}

	var ext string
	switch mimeType {
	case "image/jpeg", "image/jpg":
		ext = "jpg"
	case "image/webp":
		ext = "webp"
	case "image/gif":
		ext = "gif"
	case "image/png":
		ext = "png"
	default:
		ext = "png"
	}

	// Don't pass roomID so the media is uploaded unencrypted.
	// Since emoji images are embedded in HTML via <img> tags, the client needs
	// raw MXC access (no E2EE key material in HTML).
	mxc, _, err := intent.UploadMedia(ctx, "", data, "emoji."+ext, mimeType)
	if err != nil {
		h.Log.Warn().Err(err).Str("product_id", productID).Str("sticon_id", sticonID).Msg("Failed to upload emoji to Matrix")
		return ""
	}
	return string(mxc)
}

const (
	inlineSticonFallbackText = "[Emoji]"
	// LINE EMTVER3 sticons are one code point in the first range. EMTVER4
	// sticons are a product code point, a sticon code point, alt text, and the
	// terminator. REPLACE.sticon carries the image IDs for both formats.
	lineSticonEMTVER3RuneStart  = 0x100000
	lineSticonEMTVER3RuneEnd    = 0x1000ff
	lineSticonEMTVER4RuneStart  = 0x100100
	lineSticonEMTVER4RuneEnd    = 0x10fffe
	lineSticonEMTVER4Terminator = 0x10ffff
)

// sticonRefRegex matches inline sticon references embedded in the text body.
// LINE embeds stamps as $STK:productId:emojiId$ in the text.
var sticonRefRegex = regexp.MustCompile(`\$STK:([[:alnum:]]+):([[:alnum:]]+)\$`)

// SticonResource describes one inline sticon embedded in a text message.
// S and E are UTF-16 code unit positions in the text body that the sticon replaces.
type SticonResource struct {
	Start        int    `json:"S"`
	End          int    `json:"E"`
	ProductID    string `json:"productId"`
	SticonID     string `json:"sticonId"`
	Version      int    `json:"version"`
	ResourceType string `json:"resourceType"`
}

// replaceBody holds the REPLACE.sticon portion of the encrypted message body.
type replaceBody struct {
	Replace struct {
		Sticon struct {
			Resources []SticonResource `json:"resources"`
		} `json:"sticon"`
	} `json:"REPLACE"`
}

// ConvertInlineEmoji converts a LINE text message containing inline emoji/stamp
// to a Matrix message with text and image parts interleaved according to the
// REPLACE.sticon.resources positions in the decrypted body JSON.
// bodyText is the full decrypted JSON body, stkTxt is the unwrapped plain text.
func (h *Handler) ConvertInlineEmoji(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, data line.Message, stkTxt string, bodyText string, relatesTo *event.RelatesTo) (*bridgev2.ConvertedMessage, error) {
	stkID := data.ContentMetadata["STKID"]
	stkPkgID := data.ContentMetadata["STKPKGID"]
	sticonOwnership := data.ContentMetadata["STICON_OWNERSHIP"]

	if stkTxt == "" {
		stkTxt = data.ContentMetadata["STKTXT"]
	}
	if stkTxt == "" {
		stkTxt = inlineSticonFallbackText
	}

	// Try parsing the full bodyText as JSON — LINE embeds sticon resources
	// in the REPLACE.sticon.resources array of the encrypted message body.
	if strings.HasPrefix(bodyText, "{") {
		if resources, err := parseSticonBody(bodyText); err == nil && len(resources) > 0 {
			if msg := h.convertSticonParts(ctx, portal, intent, stkTxt, resources, relatesTo); msg != nil {
				return msg, nil
			}
		}
	}

	// Try the direct STKID/STKPKGID path after body parsing. Inline sticons may
	// also include sticker metadata, but REPLACE.sticon is the source of truth
	// for preserving surrounding text.
	if stkID != "" && stkPkgID != "" {
		if msg := h.tryDownloadSticon(ctx, portal, intent, stkPkgID, stkID, relatesTo); msg != nil {
			return msg, nil
		}
		return h.ConvertText(cleanInlineSticonPlaceholders(stkTxt), relatesTo)
	}

	// Try parsing the text body for inline sticon references ($STK:productId:emojiId$).
	if matches := sticonRefRegex.FindStringSubmatch(stkTxt); len(matches) == 3 {
		productID := matches[1]
		emojiID := matches[2]
		h.Log.Debug().Str("product_id", productID).Str("emoji_id", emojiID).Msg("Found inline sticon reference in text body")
		if msg := h.tryDownloadSticon(ctx, portal, intent, productID, emojiID, relatesTo); msg != nil {
			return msg, nil
		}
	}

	// Try resolving STICON_OWNERSHIP tokens via API as fallback.
	if sticonOwnership != "" {
		var tokens []string
		if err := json.Unmarshal([]byte(sticonOwnership), &tokens); err == nil && len(tokens) > 0 {
			token := tokens[0]

			// Try API lookup to resolve token -> product/emoji IDs
			client := h.NewClient()
			ownerships, err := client.GetSticonOwnershipByMid(data.From)
			if err == nil {
				for _, o := range ownerships {
					if o.OwnershipToken == token && o.ProductID != "" && o.EmojiID != "" {
						if msg := h.tryDownloadSticon(ctx, portal, intent, o.ProductID, o.EmojiID, relatesTo); msg != nil {
							return msg, nil
						}
					}
				}
			} else {
				h.Log.Debug().Err(err).Str("mid", data.From).Msg("GetSticonOwnershipByMid failed")
			}

			// Fallback: try the ownership token as productId with common emoji IDs
			for _, eid := range []string{"default", "1"} {
				if msg := h.tryDownloadSticon(ctx, portal, intent, token, eid, relatesTo); msg != nil {
					return msg, nil
				}
			}
		}
	}

	h.Log.Debug().
		Int("text_length", len(stkTxt)).
		Int("raw_body_length", len(bodyText)).
		Int("metadata_count", len(data.ContentMetadata)).
		Msg("Falling back to text for inline emoji")
	return h.ConvertText(cleanInlineSticonPlaceholders(stkTxt), relatesTo)
}

type sticonReplacement struct {
	start int
	end   int
	mxc   string
}

// convertSticonParts builds a single formatted text message with sticon images
// embedded inline via <img> tags (like Discord bridge custom emoji).
func (h *Handler) convertSticonParts(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, stkTxt string, resources []SticonResource, relatesTo *event.RelatesTo) *bridgev2.ConvertedMessage {
	var replacements []sticonReplacement

	for _, r := range resources {
		if r.ProductID == "" || r.SticonID == "" {
			continue
		}
		start, end, ok := sticonResourceByteRange(stkTxt, r)
		if !ok {
			h.Log.Debug().
				Int("start", r.Start).
				Int("end", r.End).
				Int("text_length", len(stkTxt)).
				Msg("Skipping inline sticon with invalid text offsets")
			continue
		}
		mxc := h.tryUploadEmoji(ctx, portal, intent, r.ProductID, r.SticonID)
		replacements = append(replacements, sticonReplacement{start: start, end: end, mxc: mxc})
	}
	if len(replacements) == 0 {
		return nil
	}

	body, formattedBody := buildSticonMessageBodies(stkTxt, replacements)
	if body == "" {
		return nil
	}

	h.Log.Debug().
		Int("body_length", len(body)).
		Str("format", string(event.FormatHTML)).
		Int("formatted_body_length", len(formattedBody)).
		Int("replacement_count", len(replacements)).
		Msg("Converted sticon parts to HTML message")

	msg := &event.MessageEventContent{
		MsgType:       event.MsgText,
		Body:          body,
		Format:        event.FormatHTML,
		FormattedBody: formattedBody,
		RelatesTo:     relatesTo,
	}

	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{
			{Type: event.EventMessage, Content: msg},
		},
	}
}

func buildSticonMessageBodies(stkTxt string, replacements []sticonReplacement) (string, string) {
	var plainBuf, htmlBuf bytes.Buffer
	pos := 0

	sort.SliceStable(replacements, func(i, j int) bool {
		return replacements[i].start < replacements[j].start
	})

	for _, r := range replacements {
		if r.start < pos || r.end <= r.start || r.start > len(stkTxt) || r.end > len(stkTxt) {
			continue
		}

		if r.start > pos {
			seg := stkTxt[pos:r.start]
			plainBuf.WriteString(seg)
			htmlBuf.WriteString(htmlEscape(seg))
		}

		placeholder := stkTxt[r.start:r.end]
		fallback := inlineSticonFallback(placeholder)
		if r.mxc != "" {
			escapedFallback := htmlEscape(fallback)
			htmlBuf.WriteString(fmt.Sprintf(`<img data-mx-emoticon src="%s" alt="%s" title="%s" height="32" />`, htmlEscape(r.mxc), escapedFallback, escapedFallback))
		} else {
			htmlBuf.WriteString(htmlEscape(fallback))
		}
		plainBuf.WriteString(fallback)

		pos = r.end
	}

	if pos < len(stkTxt) {
		seg := stkTxt[pos:]
		plainBuf.WriteString(seg)
		htmlBuf.WriteString(htmlEscape(seg))
	}

	return plainBuf.String(), htmlBuf.String()
}

func sticonResourceByteRange(text string, r SticonResource) (int, int, bool) {
	if r.End <= r.Start {
		return 0, 0, false
	}
	if start, ok := utf16OffsetToByteIndex(text, r.Start); ok {
		if end, ok := utf16OffsetToByteIndex(text, r.End); ok && end > start {
			return start, end, true
		}
	}
	return 0, 0, false
}

func utf16OffsetToByteIndex(text string, offset int) (int, bool) {
	if offset < 0 {
		return 0, false
	}
	codeUnits := 0
	for i, r := range text {
		if codeUnits == offset {
			return i, true
		}
		if r <= 0xFFFF {
			codeUnits++
		} else {
			codeUnits += 2
		}
		if codeUnits == offset {
			return i + utf8.RuneLen(r), true
		}
	}
	if codeUnits == offset {
		return len(text), true
	}
	return 0, false
}

// htmlEscape escapes special HTML characters.
func htmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}

func inlineSticonFallback(placeholder string) string {
	if placeholder == "" {
		return inlineSticonFallbackText
	}
	if cleaned := cleanInlineSticonPlaceholders(placeholder); cleaned != placeholder {
		return cleaned
	}
	return placeholder
}

// ContainsLineSticonPlaceholder reports whether text contains LINE's hidden
// EMTVER3 glyphs or a complete EMTVER4 token. Their private-use code points
// commonly render as replacement boxes in Matrix clients.
func ContainsLineSticonPlaceholder(text string) bool {
	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		if _, _, ok := lineSticonPlaceholderAt(runes, i); ok {
			return true
		}
	}
	return false
}

func cleanInlineSticonPlaceholders(text string) string {
	runes := []rune(text)
	var b strings.Builder
	for i := 0; i < len(runes); {
		if end, fallback, ok := lineSticonPlaceholderAt(runes, i); ok {
			b.WriteString(fallback)
			i = end
			continue
		}
		b.WriteRune(runes[i])
		i++
	}
	return b.String()
}

func lineSticonPlaceholderAt(runes []rune, start int) (end int, fallback string, ok bool) {
	if start < 0 || start >= len(runes) {
		return 0, "", false
	}
	if isLineSticonEMTVER3Rune(runes[start]) {
		return start + 1, inlineSticonFallbackText, true
	}
	if start+3 >= len(runes) || !isLineSticonEMTVER4Rune(runes[start]) ||
		!isLineSticonEMTVER4Rune(runes[start+1]) {
		return 0, "", false
	}
	for i := start + 2; i < len(runes); i++ {
		if runes[i] != lineSticonEMTVER4Terminator {
			continue
		}
		if i == start+2 {
			return 0, "", false
		}
		return i + 1, "(" + string(runes[start+2:i]) + ")", true
	}
	return 0, "", false
}

func isLineSticonEMTVER3Rune(r rune) bool {
	return r >= lineSticonEMTVER3RuneStart && r <= lineSticonEMTVER3RuneEnd
}

func isLineSticonEMTVER4Rune(r rune) bool {
	return r >= lineSticonEMTVER4RuneStart && r <= lineSticonEMTVER4RuneEnd
}

// parseSticonBody extracts sticon resources from the REPLACE.sticon.resources
// field of the encrypted message body JSON.
func parseSticonBody(body string) ([]SticonResource, error) {
	var rb replaceBody
	if err := json.Unmarshal([]byte(body), &rb); err != nil {
		return nil, err
	}
	if len(rb.Replace.Sticon.Resources) == 0 {
		return nil, fmt.Errorf("no sticon resources found")
	}
	for _, r := range rb.Replace.Sticon.Resources {
		if r.ProductID == "" || r.SticonID == "" {
			return nil, fmt.Errorf("incomplete sticon resource")
		}
	}
	return rb.Replace.Sticon.Resources, nil
}

// HasSticonBody reports whether a decrypted LINE JSON text body contains
// inline sticon replacement metadata.
func HasSticonBody(body string) bool {
	if !strings.HasPrefix(body, "{") {
		return false
	}
	resources, err := parseSticonBody(body)
	return err == nil && len(resources) > 0
}

// tryDownloadSticon attempts to download a sticon/stamp image from the LINE CDN
// and upload it to Matrix. Returns a ConvertedMessage on success, nil on failure.
func (h *Handler) tryDownloadSticon(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, productID, emojiID string, relatesTo *event.RelatesTo) *bridgev2.ConvertedMessage {
	url := fmt.Sprintf("https://stickershop.line-scdn.net/sticonshop/v1/sticon/%s/android/%s.png", productID, emojiID)
	resp, err := h.HTTPClient.Get(url)
	if err != nil || resp.StatusCode != 200 {
		if resp != nil {
			resp.Body.Close()
		}
		return nil
	}

	defer resp.Body.Close()
	emojiData, err := io.ReadAll(resp.Body)
	if err != nil {
		h.Log.Warn().Err(err).Str("product_id", productID).Str("emoji_id", emojiID).Msg("Failed to read sticon body")
		return nil
	}

	mimeType := resp.Header.Get("Content-Type")
	if mimeType == "" {
		mimeType = "image/png"
	}

	var ext string
	switch mimeType {
	case "image/jpeg", "image/jpg":
		ext = "jpg"
	case "image/webp":
		ext = "webp"
	case "image/gif":
		ext = "gif"
	case "image/png":
		ext = "png"
	default:
		ext = "png"
	}

	bodyName := "emoji." + ext

	mxc, file, err := intent.UploadMedia(ctx, portal.MXID, emojiData, bodyName, mimeType)
	if err != nil {
		h.Log.Warn().Err(err).Msg("Failed to upload sticon to Matrix")
		return nil
	}

	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{
			{
				Type: event.EventMessage,
				Content: &event.MessageEventContent{
					MsgType: event.MsgImage,
					Body:    bodyName,
					URL:     mxc,
					File:    file,
					Info: &event.FileInfo{
						MimeType: mimeType,
						Size:     len(emojiData),
					},
					RelatesTo: relatesTo,
				},
			},
		},
	}
}
