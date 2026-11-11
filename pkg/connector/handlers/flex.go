package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"strings"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"

	"github.com/beeper/line/pkg/line"
)

type flexComponent struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`
	Contents json.RawMessage `json:"contents"`
	Header   *flexComponent  `json:"header"`
	Hero     *flexComponent  `json:"hero"`
	Body     *flexComponent  `json:"body"`
	Footer   *flexComponent  `json:"footer"`
	Action   *struct {
		Type  string `json:"type"`
		Label string `json:"label"`
		URI   string `json:"uri"`
	} `json:"action"`
}

func (fc *flexComponent) children() []*flexComponent {
	if len(fc.Contents) == 0 {
		return nil
	}
	var list []*flexComponent
	if json.Unmarshal(fc.Contents, &list) == nil {
		return list
	}
	var single flexComponent
	if json.Unmarshal(fc.Contents, &single) == nil {
		return []*flexComponent{&single}
	}
	return nil
}

type flexLine struct {
	text string
	uri  string
}

func (fc *flexComponent) collectLines(lines []flexLine) []flexLine {
	if fc == nil {
		return lines
	}
	switch fc.Type {
	case "text":
		text := fc.Text
		if text == "" {
			var spans strings.Builder
			for _, span := range fc.children() {
				spans.WriteString(span.Text)
			}
			text = spans.String()
		}
		if text = strings.TrimSpace(text); text != "" {
			line := flexLine{text: text}
			if fc.Action != nil && fc.Action.Type == "uri" {
				line.uri = fc.Action.URI
			}
			lines = append(lines, line)
		}
		return lines
	case "button":
		if fc.Action != nil && strings.TrimSpace(fc.Action.Label) != "" {
			line := flexLine{text: strings.TrimSpace(fc.Action.Label)}
			if fc.Action.Type == "uri" {
				line.uri = fc.Action.URI
			}
			lines = append(lines, line)
		}
		return lines
	}
	for _, part := range []*flexComponent{fc.Header, fc.Hero, fc.Body, fc.Footer} {
		lines = part.collectLines(lines)
	}
	for _, child := range fc.children() {
		lines = child.collectLines(lines)
	}
	return lines
}

func isWebURL(uri string) bool {
	lower := strings.ToLower(uri)
	return strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://")
}

// ConvertFlex converts a LINE Flex message to Matrix text with its links.
func (h *Handler) ConvertFlex(data line.Message, relatesTo *event.RelatesTo) (*bridgev2.ConvertedMessage, error) {
	var root flexComponent
	var lines []flexLine
	if err := json.Unmarshal([]byte(data.ContentMetadata["FLEX_JSON"]), &root); err != nil {
		h.Log.Debug().Err(err).Str("msg_id", data.ID).Msg("Failed to parse LINE Flex JSON")
	} else {
		lines = root.collectLines(nil)
	}
	if len(lines) == 0 {
		preview := strings.TrimSpace(data.ContentMetadata["ALT_TEXT"])
		body := "LINE rich message. Check LINE for full details."
		if preview != "" {
			body += "\n\nPreview:\n" + preview
		}
		return singleTextPart(event.MsgNotice, body, "", relatesTo), nil
	}

	plain := make([]string, len(lines))
	formatted := make([]string, len(lines))
	hasLinks := false
	for i, line := range lines {
		plain[i] = line.text
		formatted[i] = html.EscapeString(line.text)
		if isWebURL(line.uri) {
			hasLinks = true
			if line.uri != line.text {
				plain[i] = fmt.Sprintf("%s: %s", line.text, line.uri)
			}
			formatted[i] = fmt.Sprintf(`<a href="%s">%s</a>`, html.EscapeString(line.uri), html.EscapeString(line.text))
		}
	}
	var formattedBody string
	if hasLinks {
		formattedBody = strings.Join(formatted, "<br>")
	}
	return singleTextPart(event.MsgText, strings.Join(plain, "\n"), formattedBody, relatesTo), nil
}

var htmlScriptRegex = regexp.MustCompile(`(?is)<(script|style|head)(\s[^>]*)?>.*?</(script|style|head)>`)

// ConvertHTML converts a LINE HTML message (content type 4) to Matrix text.
func (h *Handler) ConvertHTML(ctx context.Context, data line.Message, relatesTo *event.RelatesTo) (*bridgev2.ConvertedMessage, error) {
	body := strings.TrimSpace(data.ContentMetadata["ALT_TEXT"])
	if body == "" {
		htmlContent := htmlScriptRegex.ReplaceAllString(data.ContentMetadata["HTML_CONTENT"], "")
		body = strings.TrimSpace(format.TextHTMLParser.Parse(htmlContent, format.NewContext(ctx)))
	}
	if body == "" {
		return h.ConvertUnsupported(data, "", relatesTo)
	}
	return singleTextPart(event.MsgText, body, "", relatesTo), nil
}

// ConvertUnsupported bridges a LINE content type the bridge can't render as a
// notice, keeping whatever text LINE sent with it.
func (h *Handler) ConvertUnsupported(data line.Message, text string, relatesTo *event.RelatesTo) (*bridgev2.ConvertedMessage, error) {
	h.Log.Debug().
		Int("content_type", data.ContentType).
		Str("msg_id", data.ID).
		Interface("content_metadata", data.ContentMetadata).
		Msg("Bridging unsupported LINE content type as a notice")
	body := "Unsupported LINE message. Open LINE to view it."
	preview := strings.TrimSpace(data.ContentMetadata["ALT_TEXT"])
	if preview == "" {
		preview = strings.TrimSpace(text)
	}
	if preview != "" {
		body += "\n\n" + preview
	}
	return singleTextPart(event.MsgNotice, body, "", relatesTo), nil
}

func singleTextPart(msgType event.MessageType, body, formattedBody string, relatesTo *event.RelatesTo) *bridgev2.ConvertedMessage {
	content := &event.MessageEventContent{
		MsgType:   msgType,
		Body:      body,
		RelatesTo: relatesTo,
	}
	if formattedBody != "" {
		content.Format = event.FormatHTML
		content.FormattedBody = formattedBody
	}
	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{{Type: event.EventMessage, Content: content}},
	}
}
