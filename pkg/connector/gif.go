package connector

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

type imageContentInfo struct {
	Category  string `json:"category"`
	FileSize  int    `json:"fileSize"`
	Extension string `json:"extension"`
	Animated  bool   `json:"animated"`
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
}

func originalImageInfo(data []byte, extension string) imageContentInfo {
	info := imageContentInfo{
		Category:  "original",
		FileSize:  len(data),
		Extension: extension,
		Animated:  extension == "gif" && isAnimatedGif(data),
	}
	if config, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		info.Width, info.Height = config.Width, config.Height
	}
	return info
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

func isAnimatedGif(data []byte) bool {
	if len(data) < 13 || (string(data[:6]) != "GIF87a" && string(data[:6]) != "GIF89a") {
		return false
	}

	pos := 13
	if data[10]&0x80 != 0 {
		pos += 3 * (1 << ((data[10] & 7) + 1))
	}
	skipBlocks := func() bool {
		for pos < len(data) {
			size := int(data[pos])
			pos++
			if size == 0 {
				return true
			}
			pos += size
		}
		return false
	}
	frames := 0
	for pos < len(data) {
		switch data[pos] {
		case 0x21:
			pos += 2
			if !skipBlocks() {
				return false
			}
		case 0x2c:
			if pos+10 > len(data) {
				return false
			}
			packed := data[pos+9]
			pos += 10
			if packed&0x80 != 0 {
				pos += 3 * (1 << ((packed & 7) + 1))
			}
			pos++
			if !skipBlocks() {
				return false
			}
			frames++
			if frames > 1 {
				return true
			}
		default:
			return false
		}
	}
	return false
}

func pngAnimation(data []byte) (bool, error) {
	if !bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
		return false, nil
	}
	animated := false
	frames := 0
	duration := 0.0
	for pos := 8; pos < len(data); {
		if len(data)-pos < 12 {
			return false, fmt.Errorf("truncated PNG chunk")
		}
		length := uint64(binary.BigEndian.Uint32(data[pos : pos+4]))
		if length > uint64(len(data)-pos-12) {
			return false, fmt.Errorf("invalid PNG chunk length")
		}
		chunk := data[pos+8 : pos+8+int(length)]
		switch string(data[pos+4 : pos+8]) {
		case "acTL":
			if len(chunk) != 8 {
				return false, fmt.Errorf("invalid APNG animation control")
			}
			animated = true
		case "fcTL":
			if len(chunk) != 26 {
				return false, fmt.Errorf("invalid APNG frame control")
			}
			frames++
			den := binary.BigEndian.Uint16(chunk[22:24])
			if den == 0 {
				den = 100
			}
			delay := float64(binary.BigEndian.Uint16(chunk[20:22])) / float64(den)
			if delay == 0 {
				delay = 0.01
			}
			duration += delay
			if duration > 60 || frames > 1000 {
				return false, fmt.Errorf("APNG exceeds 60 seconds or 1000 frames")
			}
		case "IEND":
			if animated && frames == 0 {
				return false, fmt.Errorf("APNG has no frame controls")
			}
			return animated, nil
		}
		pos += int(length) + 12
	}
	return false, fmt.Errorf("PNG has no end chunk")
}

func preparePNGSticker(ctx context.Context, data []byte, sticker bool) ([]byte, error) {
	if !bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
		return data, nil
	}
	animated, err := pngAnimation(data)
	if err != nil {
		return nil, err
	}
	if !animated && !sticker {
		return data, nil
	}
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if config.Width > 8192 || config.Height > 8192 || int64(config.Width)*int64(config.Height) > 16_000_000 {
		return nil, fmt.Errorf("sticker exceeds image dimension limit")
	}
	if !animated {
		img, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		if opaque, ok := img.(interface{ Opaque() bool }); ok && opaque.Opaque() {
			return data, nil
		}
	}
	return convertPNGStickerToGIF(ctx, data, animated)
}

func convertPNGStickerToGIF(ctx context.Context, data []byte, animated bool) ([]byte, error) {
	if len(data) > maxConvertedGIFSize {
		return nil, fmt.Errorf("sticker exceeds 100 MiB")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	select {
	case gifConversionSlot <- struct{}{}:
		defer func() { <-gifConversionSlot }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	dir, err := os.MkdirTemp("", "line-png-sticker-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	input := filepath.Join(dir, "sticker.png")
	palette := filepath.Join(dir, "palette.png")
	if err = os.WriteFile(input, data, 0600); err != nil {
		return nil, err
	}
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-threads", "1"}
	duration := "60"
	if animated {
		args = append(args, "-ignore_loop", "1")
	} else {
		duration = "0.2"
		args = append(args, "-loop", "1", "-framerate", "10", "-t", duration)
	}
	args = append(args, "-i", input)
	filter := "scale=w='min(640,iw)':h='min(640,ih)':force_original_aspect_ratio=decrease:flags=lanczos"
	paletteArgs := append(slices.Clone(args),
		"-t", duration, "-vf", filter+",palettegen=reserve_transparent=1",
		"-frames:v", "1", "-threads", "1", "-update", "1", palette)
	if err = exec.CommandContext(ctx, "ffmpeg", paletteArgs...).Run(); err != nil {
		return nil, fmt.Errorf("sticker palette conversion failed: %w", err)
	}
	outArgs := append(slices.Clone(args),
		"-i", palette, "-t", "60",
		"-filter_complex", "[0:v]"+filter+"[v];[v][1:v]paletteuse=alpha_threshold=128",
		"-vsync", "0", "-enc_time_base", "1:100", "-loop", "0")
	if animated {
		delays := pngFrameDelays(data)
		outArgs = append(outArgs, "-final_delay", strconv.Itoa(delays[len(delays)-1]))
	} else {
		outArgs = append(outArgs, "-frames:v", "2")
	}
	outArgs = append(outArgs, "-f", "gif", "pipe:1")
	output := gifOutputBuffer{limit: maxConvertedGIFSize}
	cmd := exec.CommandContext(ctx, "ffmpeg", outArgs...)
	cmd.Stdout = &output
	if err = cmd.Run(); err != nil {
		return nil, fmt.Errorf("sticker GIF conversion failed: %w", err)
	}
	if !isAnimatedGif(output.buffer.Bytes()) {
		return nil, fmt.Errorf("sticker conversion did not produce a multi-frame GIF")
	}
	return output.buffer.Bytes(), nil
}

func pngFrameDelays(data []byte) []int {
	var delays []int
	for pos := 8; pos+12 <= len(data); {
		if string(data[pos+4:pos+8]) == "IEND" {
			return delays
		}
		n := int(binary.BigEndian.Uint32(data[pos:]))
		if string(data[pos+4:pos+8]) == "fcTL" {
			chunk := data[pos+8 : pos+8+n]
			num := int(binary.BigEndian.Uint16(chunk[20:22]))
			den := int(binary.BigEndian.Uint16(chunk[22:24]))
			if den == 0 {
				den = 100
			}
			delays = append(delays, max(1, (num*100+den/2)/den))
		}
		pos += n + 12
	}
	return delays
}
