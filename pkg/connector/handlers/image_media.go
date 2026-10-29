package handlers

import (
	"bytes"
	"encoding/binary"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"strings"

	_ "golang.org/x/image/webp"
	"maunium.net/go/mautrix/event"
)

// Adapted from beeper/line#189; OBS retry policy remains independent.
type lineImageMedia struct {
	fileName         string
	mimeType         string
	info             *event.FileInfo
	usedMimeFallback bool
	decodeErr        error
}

func lineImageMediaInfo(data []byte) lineImageMedia {
	mimeType, usedFallback := detectLineImageMimeType(data)
	info := &event.FileInfo{MimeType: mimeType, Size: len(data)}
	var decodeErr error
	if config, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		info.Width = config.Width
		info.Height = config.Height
	} else if !usedFallback {
		switch mimeType {
		case "image/jpeg", "image/png", "image/gif", "image/webp":
			decodeErr = err
		}
	}
	return lineImageMedia{
		fileName: "image." + imageExtensionForMIME(mimeType), mimeType: mimeType,
		info: info, usedMimeFallback: usedFallback, decodeErr: decodeErr,
	}
}

func detectLineImageMimeType(data []byte) (string, bool) {
	mimeType := normalizedImageMIMEType(http.DetectContentType(data))
	if strings.HasPrefix(mimeType, "image/") {
		return mimeType, false
	}
	if len(data) >= 12 && lineImageBrandAt(data, 4) == [4]byte{'f', 't', 'y', 'p'} {
		boxSize := binary.BigEndian.Uint32(data[:4])
		if boxSize >= 16 && uint64(boxSize) <= uint64(len(data)) {
			detected := ""
			for offset, end := 8, int(boxSize); offset+4 <= end; {
				switch lineImageBrandAt(data, offset) {
				case [4]byte{'a', 'v', 'i', 'f'}, [4]byte{'a', 'v', 'i', 's'}:
					return "image/avif", false
				case [4]byte{'h', 'e', 'i', 'c'}, [4]byte{'h', 'e', 'i', 'x'}, [4]byte{'h', 'e', 'v', 'c'}, [4]byte{'h', 'e', 'v', 'x'}:
					return "image/heic", false
				case [4]byte{'h', 'e', 'i', 'f'}, [4]byte{'h', 'e', 'i', 'm'}, [4]byte{'h', 'e', 'i', 's'}, [4]byte{'m', 'i', 'f', '1'}, [4]byte{'m', 's', 'f', '1'}:
					detected = "image/heif"
				}
				if offset == 8 {
					offset = 16
				} else {
					offset += 4
				}
			}
			if detected != "" {
				return detected, false
			}
		}
	}
	return "image/jpeg", true
}

func lineImageBrandAt(data []byte, offset int) [4]byte {
	return [4]byte{data[offset], data[offset+1], data[offset+2], data[offset+3]}
}

func imageExtensionForMIME(mimeType string) string {
	switch normalizedMIMEType := normalizedImageMIMEType(mimeType); normalizedMIMEType {
	case "image/jpeg":
		return "jpg"
	case "image/svg+xml":
		return "svg"
	case "image/x-icon", "image/vnd.microsoft.icon":
		return "ico"
	case "image/png", "image/gif", "image/webp", "image/heic", "image/heif", "image/avif", "image/bmp", "image/tiff":
		return strings.TrimPrefix(normalizedMIMEType, "image/")
	}
	return "jpg"
}

func normalizedImageMIMEType(mimeType string) string {
	normalizedMIMEType, _, _ := strings.Cut(mimeType, ";")
	return strings.ToLower(strings.TrimSpace(normalizedMIMEType))
}
