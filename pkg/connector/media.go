package connector

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	ffmpeg "github.com/u2takey/ffmpeg-go"
	mauffmpeg "go.mau.fi/util/ffmpeg"
	"golang.org/x/crypto/hkdf"
	_ "golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
	"maunium.net/go/mautrix/bridgev2"
)

const maxLineImagePixels = 100_000_000

var errSendImageAsFile = errors.New("image must be sent to LINE as a file")

func prepareLineImage(ctx context.Context, data []byte, fileName string) ([]byte, string, string, error) {
	mimeType := http.DetectContentType(data)
	switch {
	case isHEIFData(data):
		converted, err := convertHEICToJPEG(ctx, data)
		if err != nil {
			return nil, "", "", fmt.Errorf("%w: %w", bridgev2.ErrMediaConvertFailed, err)
		}
		data, mimeType, fileName = converted, "image/jpeg", replaceExtension(fileName, ".jpg")
	case mimeType == "image/jpeg", mimeType == "image/png", mimeType == "image/gif", mimeType == "image/bmp":
	case strings.HasPrefix(mimeType, "text/"):
		return nil, "", "", errSendImageAsFile
	default:
		converted, convertedMime, err := reencodeImage(ctx, data)
		if err != nil {
			return nil, "", "", fmt.Errorf("%w: %w", errSendImageAsFile, err)
		}
		data, mimeType = converted, convertedMime
		if mimeType == "image/png" {
			fileName = replaceExtension(fileName, ".png")
		} else {
			fileName = replaceExtension(fileName, ".jpg")
		}
	}
	if config, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil && config.Width*config.Height > maxLineImagePixels {
		return nil, "", "", errSendImageAsFile
	}
	return data, mimeType, fileName, nil
}

func isHEIFData(data []byte) bool {
	if len(data) < 16 || string(data[4:8]) != "ftyp" {
		return false
	}
	boxSize := int(binary.BigEndian.Uint32(data))
	if boxSize < 16 || boxSize > len(data) {
		return false
	}
	brands := []string{string(data[8:12])}
	for i := 16; i+4 <= boxSize; i += 4 {
		brands = append(brands, string(data[i:i+4]))
	}
	if slices.Contains(brands, "avif") || slices.Contains(brands, "avis") {
		return false
	}
	return slices.ContainsFunc(brands, func(brand string) bool {
		switch brand {
		case "heic", "heix", "heim", "heis", "hevc", "hevx", "mif1", "msf1":
			return true
		}
		return false
	})
}

func reencodeImage(ctx context.Context, data []byte) ([]byte, string, error) {
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		if !mauffmpeg.Supported() {
			return nil, "", err
		}
		data, err = mauffmpeg.ConvertBytes(ctx, data, ".png", nil, []string{"-frames:v", "1"}, http.DetectContentType(data))
		if err != nil {
			return nil, "", err
		}
		if config, _, err = image.DecodeConfig(bytes.NewReader(data)); err != nil {
			return nil, "", err
		}
	}
	if config.Width*config.Height > maxLineImagePixels {
		return nil, "", fmt.Errorf("image is %dx%d", config.Width, config.Height)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", err
	}
	var buf bytes.Buffer
	if opaque, ok := img.(interface{ Opaque() bool }); ok && !opaque.Opaque() {
		err = png.Encode(&buf, img)
		return buf.Bytes(), "image/png", err
	}
	err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90})
	return buf.Bytes(), "image/jpeg", err
}

func replaceExtension(fileName, extension string) string {
	if fileName == "" {
		return "image" + extension
	}
	return strings.TrimSuffix(fileName, filepath.Ext(fileName)) + extension
}

func convertHEICToJPEG(ctx context.Context, imageData []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "line-heic-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temporary HEIC directory: %w", err)
	}
	defer os.RemoveAll(dir)

	inputPath := filepath.Join(dir, "input.heic")
	if err = os.WriteFile(inputPath, imageData, 0o600); err != nil {
		return nil, fmt.Errorf("failed to write temporary HEIC file: %w", err)
	}

	// iPhone photos are HEIF grids of tiles. The ffmpeg CLI only decodes the
	// first tile, while libheif assembles the full image.
	jpegData, err := runHEIFDec(ctx, dir, inputPath, "jpg")
	if err == nil {
		return stripJPEGMetadata(jpegData), nil
	} else if ctx.Err() != nil {
		return nil, err
	}

	// libheif's JPEG writer gives up on a broken EXIF block, while its PNG
	// writer skips the block and writes the image.
	pngData, pngErr := runHEIFDec(ctx, dir, inputPath, "png")
	if pngErr != nil {
		return nil, err
	}
	img, imgErr := png.Decode(bytes.NewReader(pngData))
	if imgErr != nil {
		return nil, fmt.Errorf("failed to decode converted PNG: %w", imgErr)
	}
	var buf bytes.Buffer
	if err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		return nil, fmt.Errorf("failed to encode converted image: %w", err)
	}
	return buf.Bytes(), nil
}

func runHEIFDec(ctx context.Context, dir, inputPath, extension string) ([]byte, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "heif-dec", "--quiet", inputPath, filepath.Join(dir, "output."+extension))
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("heif-dec failed: %w", ctxErr)
		}
		return nil, fmt.Errorf("heif-dec failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	// heif-dec writes output-1.jpg, output-2.jpg, ... instead of output.jpg
	// when the file holds several top-level images. Use the first one.
	outputs, err := filepath.Glob(filepath.Join(dir, "output*."+extension))
	if err != nil || len(outputs) == 0 {
		return nil, fmt.Errorf("heif-dec produced no output: %s", strings.TrimSpace(stderr.String()))
	}
	data, err := os.ReadFile(outputs[0])
	if err != nil {
		return nil, fmt.Errorf("failed to read converted image: %w", err)
	}
	// heif-dec exits 0 even when its writer fails, leaving an empty file.
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return nil, fmt.Errorf("heif-dec produced invalid %s output: %s", extension, strings.TrimSpace(stderr.String()))
	}
	return data, nil
}

// stripJPEGMetadata drops every APP1 segment. heif-dec copies the HEIC's EXIF
// (split over several APP1 segments when large) and XMP into them, including
// GPS location and device model. Rotation is already applied to the pixels.
func stripJPEGMetadata(data []byte) []byte {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return data
	}
	out := make([]byte, 0, len(data))
	out = append(out, data[:2]...)
	pos := 2
	for pos+4 <= len(data) && data[pos] == 0xFF {
		marker := data[pos+1]
		if marker == 0xDA {
			break
		}
		length := int(data[pos+2])<<8 | int(data[pos+3])
		end := pos + 2 + length
		if length < 2 || end > len(data) {
			return data
		}
		if marker != 0xE1 {
			out = append(out, data[pos:end]...)
		}
		pos = end
	}
	return append(out, data[pos:]...)
}

// AES-256-CTR
// LINE's E2EE file format: [encrypted_data][32-byte HMAC]
// The keyMaterial is derived using HKDF to get encKey (32), macKey (32), and nonce (12 bytes)
func (lc *LineClient) decryptImageData(encryptedData []byte, keyMaterialB64 string) ([]byte, error) {
	keyMaterial, err := base64.StdEncoding.DecodeString(keyMaterialB64)
	if err != nil {
		return nil, fmt.Errorf("failed to decode key material: %w", err)
	}

	// Derive keys using HKDF (SHA-256, no salt, info="FileEncryption")
	// Derives 76 bytes: encKey(32) + macKey(32) + nonce(12)
	kdf := hkdf.New(sha256.New, keyMaterial, nil, []byte("FileEncryption"))
	derived := make([]byte, 76)
	if _, err := io.ReadFull(kdf, derived); err != nil {
		return nil, fmt.Errorf("failed to derive keys: %w", err)
	}

	encKey := derived[0:32]
	// macKey := derived[32:64] // for HMAC verification
	nonce := derived[64:76]

	// Create 16-byte counter: nonce(12 bytes) + zero counter(4 bytes)
	counter := make([]byte, 16)
	copy(counter, nonce)
	// Last 4 bytes remain zero

	if len(encryptedData) < 32 {
		return nil, fmt.Errorf("encrypted data too short (< 32 bytes for HMAC)")
	}
	encryptedData = encryptedData[:len(encryptedData)-32]

	block, err := aes.NewCipher(encKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create AES cipher: %w", err)
	}

	stream := cipher.NewCTR(block, counter)

	decrypted := make([]byte, len(encryptedData))
	stream.XORKeyStream(decrypted, encryptedData)

	return decrypted, nil
}

// AES-256-CTR
func (lc *LineClient) encryptFileData(plainData []byte) ([]byte, string, error) {
	keyMaterial := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, keyMaterial); err != nil {
		return nil, "", fmt.Errorf("failed to generate key material: %w", err)
	}

	// Derive keys using HKDF (SHA-256, no salt, info="FileEncryption")
	// Derives 76 bytes: encKey(32) + macKey(32) + nonce(12)
	kdf := hkdf.New(sha256.New, keyMaterial, nil, []byte("FileEncryption"))
	derived := make([]byte, 76)
	if _, err := io.ReadFull(kdf, derived); err != nil {
		return nil, "", fmt.Errorf("failed to derive keys: %w", err)
	}

	encKey := derived[0:32]
	macKey := derived[32:64]
	nonce := derived[64:76]

	// Create 16-byte counter: nonce(12 bytes) + zero counter(4 bytes)
	counter := make([]byte, 16)
	copy(counter, nonce)

	block, err := aes.NewCipher(encKey)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create AES cipher: %w", err)
	}

	stream := cipher.NewCTR(block, counter)

	encrypted := make([]byte, len(plainData))
	stream.XORKeyStream(encrypted, plainData)

	h := hmac.New(sha256.New, macKey)
	h.Write(encrypted)
	hmacSum := h.Sum(nil)

	// LINE E2EE file format: [encrypted_data][32-byte HMAC]
	result := append(encrypted, hmacSum...)

	keyMaterialB64 := base64.StdEncoding.EncodeToString(keyMaterial)

	return result, keyMaterialB64, nil
}

// encryptVideoData encrypts video data with HMAC computed on chunk hashes
func (lc *LineClient) encryptVideoData(plainData []byte) ([]byte, string, error) {
	keyMaterial := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, keyMaterial); err != nil {
		return nil, "", fmt.Errorf("failed to generate key material: %w", err)
	}

	kdf := hkdf.New(sha256.New, keyMaterial, nil, []byte("FileEncryption"))
	derived := make([]byte, 76)
	if _, err := io.ReadFull(kdf, derived); err != nil {
		return nil, "", fmt.Errorf("failed to derive keys: %w", err)
	}

	encKey := derived[0:32]
	macKey := derived[32:64]
	nonce := derived[64:76]

	counter := make([]byte, 16)
	copy(counter, nonce)

	block, err := aes.NewCipher(encKey)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create AES cipher: %w", err)
	}

	stream := cipher.NewCTR(block, counter)

	encrypted := make([]byte, len(plainData))
	stream.XORKeyStream(encrypted, plainData)

	// For videos: compute HMAC on chunk hashes
	chunkHashes := generateChunkHashes(encrypted)
	h := hmac.New(sha256.New, macKey)
	h.Write(chunkHashes)
	hmacSum := h.Sum(nil)

	result := append(encrypted, hmacSum...)
	keyMaterialB64 := base64.StdEncoding.EncodeToString(keyMaterial)

	return result, keyMaterialB64, nil
}

func generateThumbnail(imageData []byte) ([]byte, int, int, error) {
	img, _, err := image.Decode(bytes.NewReader(imageData))
	if err != nil {
		return nil, 0, 0, fmt.Errorf("failed to decode image: %w", err)
	}

	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	maxDim := 1280
	newWidth := width
	newHeight := height

	if width > maxDim || height > maxDim {
		if width > height {
			newWidth = maxDim
			newHeight = (height * maxDim) / width
		} else {
			newHeight = maxDim
			newWidth = (width * maxDim) / height
		}
	}

	var thumbnail image.Image
	if newWidth != width || newHeight != height {
		thumbnail = image.NewRGBA(image.Rect(0, 0, newWidth, newHeight))
		draw.CatmullRom.Scale(thumbnail.(draw.Image), thumbnail.Bounds(), img, bounds, draw.Over, nil)
	} else {
		thumbnail = img
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, thumbnail, &jpeg.Options{Quality: 60}); err != nil {
		return nil, 0, 0, fmt.Errorf("failed to encode thumbnail: %w", err)
	}

	return buf.Bytes(), newWidth, newHeight, nil
}

// encryptThumbnail encrypts thumbnail data using the same key material as the parent media.
// Returns the encrypted thumbnail with HMAC appended, matching LINE's E2EE thumbnail format.
func encryptThumbnail(thumbnailData []byte, keyMaterialB64 string) ([]byte, error) {
	keyMaterial, err := base64.StdEncoding.DecodeString(keyMaterialB64)
	if err != nil {
		return nil, fmt.Errorf("failed to decode key material: %w", err)
	}

	kdf := hkdf.New(sha256.New, keyMaterial, nil, []byte("FileEncryption"))
	derived := make([]byte, 76)
	if _, err := io.ReadFull(kdf, derived); err != nil {
		return nil, fmt.Errorf("failed to derive keys: %w", err)
	}

	encKey := derived[0:32]
	macKey := derived[32:64]
	nonce := derived[64:76]

	counter := make([]byte, 16)
	copy(counter, nonce)

	block, err := aes.NewCipher(encKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create cipher: %w", err)
	}
	stream := cipher.NewCTR(block, counter)

	encrypted := make([]byte, len(thumbnailData))
	stream.XORKeyStream(encrypted, thumbnailData)

	h := hmac.New(sha256.New, macKey)
	h.Write(encrypted)
	return append(encrypted, h.Sum(nil)...), nil
}

func isAnimatedGif(data []byte) bool {
	// GIF header: "GIF89a" or "GIF87a"
	if len(data) < 6 {
		return false
	}

	if string(data[0:3]) != "GIF" {
		return false
	}

	// Count image descriptors (0x2C) which indicate frames
	frameCount := 0
	for i := 0; i < len(data)-1; i++ {
		if data[i] == 0x2C { // Image descriptor separator
			frameCount++
			if frameCount > 1 {
				return true
			}
		}
	}

	return false
}

// generates the first frame of a video and resizes it to fit within 384x384
func extractVideoThumbnail(videoData []byte) ([]byte, int, int, error) {
	tmpVideoFile, err := os.CreateTemp("", "video-*.mp4")
	if err != nil {
		return nil, 0, 0, fmt.Errorf("failed to create temp video file: %w", err)
	}
	defer os.Remove(tmpVideoFile.Name())

	if _, err := tmpVideoFile.Write(videoData); err != nil {
		return nil, 0, 0, fmt.Errorf("failed to write video data: %w", err)
	}
	tmpVideoFile.Close()

	tmpThumbFile, err := os.CreateTemp("", "thumb-*.jpg")
	if err != nil {
		return nil, 0, 0, fmt.Errorf("failed to create temp thumb file: %w", err)
	}
	defer os.Remove(tmpThumbFile.Name())
	tmpThumbFile.Close()

	err = ffmpeg.Input(tmpVideoFile.Name()).
		Filter("scale", ffmpeg.Args{"384:384:force_original_aspect_ratio=decrease"}).
		Output(tmpThumbFile.Name(), ffmpeg.KwArgs{
			"vframes": 1,
			"q:v":     5,
		}).
		OverWriteOutput().
		Silent(true).
		Run()

	if err != nil {
		return nil, 0, 0, fmt.Errorf("ffmpeg failed: %w", err)
	}

	thumbData, err := os.ReadFile(tmpThumbFile.Name())
	if err != nil {
		return nil, 0, 0, fmt.Errorf("failed to read thumbnail: %w", err)
	}

	img, _, err := image.Decode(bytes.NewReader(thumbData))
	if err != nil {
		return nil, 0, 0, fmt.Errorf("failed to decode thumbnail: %w", err)
	}

	bounds := img.Bounds()
	return thumbData, bounds.Dx(), bounds.Dy(), nil
}

// generateChunkHashes generates SHA-256 hashes for 128KB chunks of encrypted video data
// This is required by LINE for video integrity verification
func generateChunkHashes(encryptedData []byte) []byte {
	const chunkSize = 131072 // 128KB chunks
	var allHashes []byte

	for i := 0; i < len(encryptedData); i += chunkSize {
		end := i + chunkSize
		if end > len(encryptedData) {
			end = len(encryptedData)
		}

		chunk := encryptedData[i:end]
		hash := sha256.Sum256(chunk)
		allHashes = append(allHashes, hash[:]...)
	}

	return allHashes
}
