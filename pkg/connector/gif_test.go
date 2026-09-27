package connector

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/gif"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func testGIF(t *testing.T, frames int) []byte {
	t.Helper()

	palette := color.Palette{color.RGBA{44, 44, 44, 255}, color.White}
	animation := &gif.GIF{LoopCount: 0}
	for range frames {
		animation.Image = append(animation.Image, image.NewPaletted(image.Rect(0, 0, 8, 6), palette))
		animation.Delay = append(animation.Delay, 10)
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, animation); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestOriginalGIFInfo(t *testing.T) {
	for _, frames := range []int{1, 2} {
		data := testGIF(t, frames)
		info := originalImageInfo(data, "gif")
		if info.Category != "original" || info.Extension != "gif" || info.FileSize != len(data) ||
			info.Width != 8 || info.Height != 6 || info.Animated != (frames > 1) {
			t.Fatalf("frames=%d: unexpected metadata: %+v", frames, info)
		}
	}
}

func TestGIFFrameDetectionIgnoresCommentsAndTruncation(t *testing.T) {
	data := testGIF(t, 1)

	commented := append(bytes.Clone(data[:len(data)-1]), 0x21, 0xfe, 3, 0x2c, 0x2c, 0x2c, 0, 0x3b)
	if isAnimatedGif(commented) {
		t.Fatal("static GIF with comment was detected as animated")
	}
	animated := testGIF(t, 2)
	for i := range len(animated) {

		isAnimatedGif(animated[:i])
	}
	for _, bad := range [][]byte{nil, []byte("GIFxxx,,"), animated[:13]} {
		if isAnimatedGif(bad) {
			t.Fatalf("invalid GIF detected as animated: %x", bad)
		}
	}
}

func TestConvertVideoToGIF(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	path := filepath.Join(t.TempDir(), "animation.mp4")
	cmd := exec.CommandContext(t.Context(), "ffmpeg", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=64x48:rate=10:duration=0.5",
		"-c:v", "mpeg4", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create video fixture: %v: %s", err, out)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	converted, err := convertVideoToGIF(t.Context(), data)
	if err != nil {
		t.Fatal(err)
	}
	animation, err := gif.DecodeAll(bytes.NewReader(converted))
	if err != nil {
		t.Fatal(err)
	}
	if len(animation.Image) < 2 || animation.LoopCount != 0 || animation.Config.Width != 64 || animation.Config.Height != 48 {
		t.Fatalf("conversion did not preserve a looping animation: frames=%d loops=%d config=%+v",
			len(animation.Image), animation.LoopCount, animation.Config)
	}
	if _, err := convertVideoToGIF(t.Context(), []byte("not a video")); err == nil {
		t.Fatal("expected invalid video to fail")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := convertVideoToGIF(ctx, data); err != context.Canceled {
		t.Fatalf("canceled conversion: %v", err)
	}
}

func TestGIFOutputLimit(t *testing.T) {
	output := gifOutputBuffer{limit: 4}
	if _, err := output.Write([]byte("GIF")); err != nil {
		t.Fatal(err)
	}
	if _, err := output.Write([]byte("89a")); err == nil {
		t.Fatal("oversized output accepted")
	}
	if output.buffer.Len() != 3 {
		t.Fatal("oversized write changed the buffer")
	}
}

func TestConvertLongVideoToGIF(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	path := filepath.Join(t.TempDir(), "long.mp4")
	cmd := exec.CommandContext(t.Context(), "ffmpeg", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=c=red:size=640x640:rate=15:duration=20", "-c:v", "mpeg4", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v: %s", err, out)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	converted, err := convertVideoToGIF(t.Context(), data)
	if err != nil {
		t.Fatal(err)
	}
	animation, err := gif.DecodeAll(bytes.NewReader(converted))
	if err != nil {
		t.Fatal(err)
	}
	duration := 0
	for _, delay := range animation.Delay {
		duration += delay
	}
	if duration < 1900 || duration > 2100 {
		t.Fatalf("animation duration = %d centiseconds", duration)
	}

	path = filepath.Join(t.TempDir(), "too-long.mp4")
	cmd = exec.CommandContext(t.Context(), "ffmpeg", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=size=16x16:rate=1:duration=61", "-c:v", "mpeg4", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v: %s", err, out)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := convertVideoToGIF(t.Context(), data); err == nil {
		t.Fatal("long input accepted")
	}
}

func TestGIFOutputLimitThroughCopy(t *testing.T) {
	output := gifOutputBuffer{limit: 4}

	input := struct{ io.Reader }{bytes.NewBufferString("GIF89a")}
	if _, err := io.Copy(&output, input); err == nil {
		t.Fatal("io.Copy bypassed output limit")
	}
	if output.buffer.Len() > output.limit {
		t.Fatal("output exceeded limit")
	}
}
