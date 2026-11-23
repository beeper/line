package handlers

import (
	"encoding/json"
	"fmt"
	"strings"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"

	"github.com/beeper/line/pkg/line"
)

// ConvertLocation converts a LINE location message to a Matrix location message.
func (h *Handler) ConvertLocation(data line.Message, decryptedBody string, relatesTo *event.RelatesTo) (*bridgev2.ConvertedMessage, error) {
	location := data.Location
	if location == nil && strings.HasPrefix(strings.TrimSpace(decryptedBody), "{") {
		var payload struct {
			Location *struct {
				Title     string       `json:"title"`
				Address   string       `json:"address"`
				Latitude  *json.Number `json:"latitude"`
				Longitude *json.Number `json:"longitude"`
			} `json:"location"`
		}
		if err := json.Unmarshal([]byte(decryptedBody), &payload); err != nil {
			h.Log.Warn().Err(err).Str("msg_id", data.ID).Msg("Failed to parse encrypted location payload")
		} else if loc := payload.Location; loc != nil && loc.Latitude != nil && loc.Longitude != nil {
			lat, latErr := loc.Latitude.Float64()
			long, longErr := loc.Longitude.Float64()
			if latErr == nil && longErr == nil && lat >= -90 && lat <= 90 && long >= -180 && long <= 180 {
				location = &line.MessageLocation{
					Title: loc.Title, Address: loc.Address,
					Latitude: lat, Longitude: long,
				}
			}
		}
	}
	if location == nil {
		return &bridgev2.ConvertedMessage{
			Parts: []*bridgev2.ConvertedMessagePart{
				{
					Type: event.EventMessage,
					Content: &event.MessageEventContent{
						MsgType: event.MsgNotice,
						Body:    "[Location unavailable]",
					},
				},
			},
		}, nil
	}

	geoURI := fmt.Sprintf("geo:%.6f,%.6f", location.Latitude, location.Longitude)
	mapsURL := fmt.Sprintf("https://maps.google.com/maps?q=%.6f%%2C%.6f", location.Latitude, location.Longitude)

	var bodyParts []string
	if location.Title != "" {
		bodyParts = append(bodyParts, location.Title)
	}
	if location.Address != "" {
		bodyParts = append(bodyParts, location.Address)
	}
	bodyParts = append(bodyParts, mapsURL)
	body := strings.Join(bodyParts, "\n")

	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{
			{
				Type: event.EventMessage,
				Content: &event.MessageEventContent{
					MsgType:   event.MsgLocation,
					Body:      body,
					GeoURI:    geoURI,
					RelatesTo: relatesTo,
				},
			},
		},
	}, nil
}
