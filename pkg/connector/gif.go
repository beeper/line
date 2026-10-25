package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// maxGIFSize is the largest GIF LINE sends as an image. The LINE Chrome
// Extension sends bigger ones as files ("Photos larger than 20 MB can't be sent
// in their original resolution"), so the bridge refuses them as too large.
const maxGIFSize = 20 * 1000 * 1000

// imageContentInfo is MEDIA_CONTENT_INFO as the LINE Chrome Extension sends it
// for images: the original file's size and pixel dimensions, and for GIFs
// whether they have more than one frame.
type imageContentInfo struct {
	Category  string `json:"category"`
	FileSize  int    `json:"fileSize"`
	Extension string `json:"extension"`
	Animated  *bool  `json:"animated,omitempty"`
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
}

func originalImageInfo(data []byte, extension string) imageContentInfo {
	info := imageContentInfo{
		Category:  "original",
		FileSize:  len(data),
		Extension: extension,
	}
	if extension == "gif" {
		animated := isAnimatedGif(data)
		info.Animated = &animated
	}
	if config, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		info.Width, info.Height = config.Width, config.Height
	}
	return info
}

// imageMediaMetadata builds the content metadata the LINE Chrome Extension
// sends with an image. Encrypted sends add OID, SID, FILE_SIZE and the
// thumbnail info; plain sends carry MEDIA_CONTENT_INFO only. Neither sends
// FILE_NAME or a contentType key.
func imageMediaMetadata(data []byte, extension string, plain bool) map[string]string {
	metadata := map[string]string{}
	if infoJSON, err := json.Marshal(originalImageInfo(data, extension)); err == nil {
		metadata["MEDIA_CONTENT_INFO"] = string(infoJSON)
	}
	if !plain {
		metadata["FILE_SIZE"] = strconv.Itoa(len(data))
	}
	return metadata
}

const maxConvertedGIFSize = 100 * 1024 * 1024

var gifConversionSlot = make(chan struct{}, 1)

type gifOutputBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (b *gifOutputBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.buffer.Len() {
		return 0, fmt.Errorf("converted GIF exceeds %d byte limit", b.limit)
	}
	return b.buffer.Write(p)
}

// convertVideoToGIF turns the MP4 or WebM that Matrix GIF pickers send
// (fi.mau.gif) into a real GIF, because LINE only receives GIFs as images.
func convertVideoToGIF(ctx context.Context, data []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(data) > maxConvertedGIFSize {
		return nil, fmt.Errorf("GIF video exceeds 100 MiB input limit")
	}
	select {
	case gifConversionSlot <- struct{}{}:
		defer func() { <-gifConversionSlot }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	dir, err := os.MkdirTemp("", "line-gif-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	input := filepath.Join(dir, "input")
	palette := filepath.Join(dir, "palette.png")
	if err = os.WriteFile(input, data, 0600); err != nil {
		return nil, err
	}
	probe := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", input)
	durationData, err := probe.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("failed to inspect GIF video (ffprobe must be installed): %w", err)
	}
	duration, err := strconv.ParseFloat(strings.TrimSpace(string(durationData)), 64)
	if err != nil || math.IsNaN(duration) || math.IsInf(duration, 0) || duration <= 0 || duration > 60 {
		return nil, fmt.Errorf("GIF video must have a known duration of at most 60 seconds")
	}
	filter := "fps=15,scale=w='min(640,iw)':h='min(640,ih)':force_original_aspect_ratio=decrease:flags=lanczos"

	makePalette := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error", "-nostdin",
		"-threads", "1", "-i", input, "-an", "-vf", filter+",palettegen",
		"-frames:v", "1", "-threads", "1", "-update", "1", palette)
	if err = makePalette.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("ffmpeg GIF palette generation failed: %w", err)
	}
	output := gifOutputBuffer{limit: maxConvertedGIFSize}
	cmd := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error", "-nostdin",
		"-threads", "1", "-i", input, "-i", palette, "-an",
		"-filter_complex", "[0:v]"+filter+"[v];[v][1:v]paletteuse",
		"-loop", "0", "-f", "gif", "pipe:1")
	cmd.Stdout = &output
	if err = cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("ffmpeg GIF conversion failed: %w", err)
	}
	converted := output.buffer.Bytes()
	if _, format, err := image.DecodeConfig(bytes.NewReader(converted)); err != nil || format != "gif" {
		return nil, fmt.Errorf("ffmpeg produced invalid GIF output")
	}
	return converted, nil
}
