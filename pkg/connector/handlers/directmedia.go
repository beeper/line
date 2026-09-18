package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

const directMediaMetadataVersion = 1

type DirectMedia struct {
	Version     int      `json:"version"`
	Kind        string   `json:"kind"`
	OID         string   `json:"oid,omitempty"`
	PublicPath  string   `json:"public_path,omitempty"`
	SID         string   `json:"sid,omitempty"`
	TID         string   `json:"tid,omitempty"`
	OBSPop      string   `json:"obs_pop,omitempty"`
	ContentType string   `json:"content_type,omitempty"`
	Keys        []string `json:"keys,omitempty"`
	Size        int      `json:"-"`
	FileName    string   `json:"-"`
	Duration    int      `json:"-"`
}

type GenerateDirectMediaURI func(context.Context, string, *DirectMedia) (id.ContentURIString, any, error)

func newDirectMedia(
	kind, oid, publicPath, sid string,
	opts line.OBSDownloadOptions,
	decryptedBody string,
	contentMetadata map[string]string,
) (*DirectMedia, error) {
	kind = strings.ToLower(kind)
	keys, encrypted, err := mediaDecryptionKeys(decryptedBody, contentMetadata, kind)
	if err != nil {
		return nil, err
	}
	fileName, contentType, err := directMediaContent(kind, decryptedBody, contentMetadata)
	if err != nil {
		return nil, err
	}
	metadata := &DirectMedia{
		Version:     directMediaMetadataVersion,
		Kind:        kind,
		OID:         oid,
		PublicPath:  publicPath,
		SID:         sid,
		TID:         opts.TID,
		OBSPop:      opts.OBSPop,
		ContentType: contentType,
		Keys:        keys,
		Size:        directMediaReportedSize(contentMetadata, encrypted),
		FileName:    fileName,
		Duration:    directMediaDuration(contentMetadata),
	}
	return metadata, metadata.validate()
}

func (dm *DirectMedia) validate() error {
	if dm == nil || dm.Version != directMediaMetadataVersion {
		return fmt.Errorf("unsupported or missing LINE direct media metadata")
	}
	switch dm.Kind {
	case "image", "video", "audio", "file":
	default:
		return fmt.Errorf("unsupported LINE direct media kind %q", dm.Kind)
	}
	if (dm.OID == "") == (dm.PublicPath == "") || (dm.PublicPath == "" && dm.SID == "") {
		return fmt.Errorf("invalid LINE direct media source")
	}
	if len(dm.PublicPath) > 4096 || len(dm.Keys) > 2 {
		return fmt.Errorf("LINE direct media metadata is too large")
	}
	for _, value := range []string{dm.OID, dm.SID, dm.TID, dm.OBSPop, dm.ContentType} {
		if len(value) > 512 {
			return fmt.Errorf("LINE direct media metadata is too large")
		}
	}
	for _, key := range dm.Keys {
		if key == "" || len(key) > 1024 {
			return fmt.Errorf("LINE direct media has an invalid media key")
		}
	}
	return nil
}

func (h *Handler) generateDirectMedia(
	ctx context.Context,
	messageID string,
	metadata *DirectMedia,
	content *event.MessageEventContent,
) (*bridgev2.ConvertedMessage, error) {
	if h.GenerateDirectMediaURI == nil {
		return nil, nil
	}
	uri, dbMetadata, err := h.GenerateDirectMediaURI(ctx, messageID, metadata)
	if err != nil {
		return nil, err
	}
	content.URL = uri
	content.Body = metadata.FileName
	if content.Info == nil {
		content.Info = &event.FileInfo{}
	}
	content.Info.MimeType = metadata.ContentType
	content.Info.Size = metadata.Size
	content.Info.Duration = metadata.Duration
	return &bridgev2.ConvertedMessage{Parts: []*bridgev2.ConvertedMessagePart{{
		Type: event.EventMessage, Content: content, DBMetadata: dbMetadata,
	}}}, nil
}

func directMediaReportedSize(contentMetadata map[string]string, encrypted bool) int {
	size, err := strconv.ParseInt(strings.TrimSpace(contentMetadata["FILE_SIZE"]), 10, 64)
	if err != nil || size <= 0 {
		return 0
	}
	if encrypted && size >= encryptedMediaSizeOverhead {
		size -= encryptedMediaSizeOverhead
	}
	if int64(int(size)) != size {
		return 0
	}
	return int(size)
}

func directMediaDuration(contentMetadata map[string]string) int {
	duration, _ := strconv.Atoi(contentMetadata["DURATION"])
	return duration
}

func directMediaContent(kind, decryptedBody string, metadata map[string]string) (name, contentType string, err error) {
	switch kind {
	case "image":
		return "image.jpg", "image/jpeg", nil
	case "audio":
		return "audio.m4a", "audio/mp4", nil
	case "video":
		name, contentType = metadata["FILE_NAME"], "video/mp4"
	case "file":
		contentType = "application/octet-stream"
	}
	if name == "" && strings.Contains(decryptedBody, "fileName") {
		var payload struct {
			FileName string `json:"fileName"`
		}
		if err = json.Unmarshal([]byte(decryptedBody), &payload); err != nil && kind == "file" {
			return "", "", fmt.Errorf("failed to parse file payload: %w", err)
		}
		name = payload.FileName
	}
	if name == "" {
		name = metadata["FILE_NAME"]
	}
	if name == "" {
		name = map[string]string{"video": "video.mp4", "file": "file.bin"}[kind]
	}
	if strings.HasSuffix(strings.ToLower(name), ".webm") {
		contentType = "video/webm"
	} else if strings.HasSuffix(strings.ToLower(name), ".pdf") {
		contentType = "application/pdf"
	}
	return name, contentType, nil
}

func (h *Handler) DownloadDirectMedia(ctx context.Context, messageID string, metadata *DirectMedia) ([]byte, error) {
	if err := metadata.validate(); err != nil {
		return nil, err
	}
	client := h.NewClient()
	if client == nil {
		return nil, fmt.Errorf("LINE media client is unavailable")
	}

	var data []byte
	var err error
	if metadata.PublicPath != "" {
		data, err = client.DownloadOBSPublicResource(ctx, metadata.PublicPath)
	} else {
		if metadata.SID == "m" {
			messageID = ""
		}
		download := func(downloadClient *line.Client) ([]byte, error) {
			return downloadClient.DownloadOBSWithSIDOptions(ctx, metadata.OID, messageID, metadata.SID, line.OBSDownloadOptions{
				TID: metadata.TID, OBSPop: metadata.OBSPop,
			})
		}
		data, err = download(client)
		if newClient, ok := h.tryRecoverClient(ctx, client, err); ok {
			client = newClient
			data, err = download(client)
		}
		h.handleFinalAuthError(ctx, client, err)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to download %s from LINE OBS: %w", metadata.Kind, err)
	}
	return h.decryptMediaWithKeys(data, metadata.Keys, len(metadata.Keys) > 0, metadata.Kind)
}
