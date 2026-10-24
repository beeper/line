package connector

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"os"
	"os/exec"
	"strings"
	"unicode/utf16"

	"github.com/rs/zerolog"
	_ "golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"github.com/beeper/line/pkg/line"
)

// The Chrome extension cuts group names to 50 characters (JS string length,
// so UTF-16 code units) before calling updateChat.
const maxLineGroupNameLength = 50

const avatarJPEGQuality = 90

var (
	_ bridgev2.RoomNameHandlingNetworkAPI   = (*LineClient)(nil)
	_ bridgev2.RoomAvatarHandlingNetworkAPI = (*LineClient)(nil)
)

func groupMetaError(err error) error {
	return bridgev2.WrapErrorInStatus(err).
		WithIsCertain(true).
		WithSendNotice(true).
		WithErrorAsMessage().
		WithErrorReason(event.MessageStatusUnsupported).
		WithStatus(event.MessageStatusFail)
}

func avatarConvertError(err error) error {
	return bridgev2.WrapErrorInStatus(err).
		WithIsCertain(true).
		WithSendNotice(true).
		WithStatus(event.MessageStatusFail).
		WithMessage("couldn't convert this image to a LINE group photo")
}

var (
	errGroupMetaUnsupported = groupMetaError(errors.New("only LINE groups can be renamed or have their photo changed"))
	errGroupNameEmpty       = groupMetaError(errors.New("LINE group names can't be empty"))
	errGroupNameTooLong     = groupMetaError(fmt.Errorf("LINE group names can be at most %d characters", maxLineGroupNameLength))
)

// isEditableGroupMID reports whether a chat's name and photo can be changed.
// Only groups (C...) can: legacy rooms (R...) have no name or photo of their own.
func isEditableGroupMID(mid string) bool {
	return strings.HasPrefix(strings.ToLower(mid), "c")
}

func lineGroupPowerLevels() *bridgev2.PowerLevelOverrides {
	// Any LINE group member can rename the group or change its photo.
	return &bridgev2.PowerLevelOverrides{
		Events: map[event.Type]int{
			event.StateRoomName:   0,
			event.StateRoomAvatar: 0,
		},
	}
}

func validateLineGroupName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errGroupNameEmpty
	}
	if len(utf16.Encode([]rune(name))) > maxLineGroupNameLength {
		return errGroupNameTooLong
	}
	return nil
}

func (lc *LineClient) HandleMatrixRoomName(ctx context.Context, msg *bridgev2.MatrixRoomName) (bool, error) {
	chatMid := string(msg.Portal.ID)
	if !isEditableGroupMID(chatMid) {
		return false, errGroupMetaUnsupported
	}
	name := msg.Content.Name
	if err := validateLineGroupName(name); err != nil {
		return false, err
	}
	err := lc.updateGroupChat(ctx, chatMid, line.ChatAttributeName, func(chat map[string]any) {
		chat["chatName"] = name
	})
	if err != nil {
		return false, wrapSendError(err)
	}
	msg.Portal.Name = name
	msg.Portal.NameSet = true
	return true, nil
}

func (lc *LineClient) HandleMatrixRoomAvatar(ctx context.Context, msg *bridgev2.MatrixRoomAvatar) (bool, error) {
	chatMid := string(msg.Portal.ID)
	if !isEditableGroupMID(chatMid) {
		return false, errGroupMetaUnsupported
	}
	if msg.Content.URL == "" && msg.Content.MSC3414File == nil {
		err := lc.updateGroupChat(ctx, chatMid, line.ChatAttributePicture, func(chat map[string]any) {
			chat["picturePath"] = ""
		})
		if err != nil {
			return false, wrapSendError(err)
		}
		msg.Portal.AvatarID = ""
		msg.Portal.AvatarMXC = ""
		msg.Portal.AvatarHash = [32]byte{}
		msg.Portal.AvatarSet = true
		return true, nil
	}

	avatarURL := msg.Content.URL
	if msg.Content.MSC3414File != nil {
		avatarURL = msg.Content.MSC3414File.URL
	}
	data, err := lc.UserLogin.Bridge.Bot.DownloadMedia(ctx, avatarURL, msg.Content.MSC3414File)
	if err != nil {
		return false, avatarConvertError(fmt.Errorf("failed to download avatar: %w", err))
	}
	jpegData, err := convertAvatarToJPEG(ctx, data)
	if err != nil {
		return false, avatarConvertError(err)
	}
	_, err = lc.callLine(ctx, func(client *line.Client) error {
		return client.UploadChatPicture(ctx, chatMid, jpegData)
	})
	if err != nil {
		return false, wrapSendError(err)
	}

	// Like the extension, fetch the chat again after the change. The new
	// picturePath is the avatar ID the op 121 resync will report, so storing it
	// keeps that resync from bridging the same picture back to Matrix.
	_, chats, err := callLineResult(lc, ctx, func(client *line.Client) (*line.GetChatsResponse, error) {
		return client.GetChats([]string{chatMid}, false, false)
	})
	var picturePath string
	if err == nil && len(chats.Chats) > 0 {
		picturePath = chats.Chats[0].PicturePath
	} else {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to fetch group picture path after upload")
	}
	msg.Portal.AvatarID = networkid.AvatarID(picturePath)
	msg.Portal.AvatarMXC = avatarURL
	msg.Portal.AvatarHash = sha256.Sum256(data)
	msg.Portal.AvatarSet = true
	return true, nil
}

// updateGroupChat round-trips the chat object from getChats through updateChat
// with one field edited, which is what the Chrome extension sends.
func (lc *LineClient) updateGroupChat(ctx context.Context, chatMid string, attribute line.ChatAttribute, edit func(chat map[string]any)) error {
	reqSeq := int64(lc.nextUntrackedReqSeq())
	_, err := lc.callLine(ctx, func(client *line.Client) error {
		chat, err := client.GetRawChat(ctx, chatMid)
		if err != nil {
			return err
		}
		edit(chat)
		return client.UpdateChat(ctx, reqSeq, chat, attribute)
	})
	return err
}

// LINE accepted a 4096×4096 photo from the extension, so bigger photos are
// scaled down to fit. Decoding needs about 4 bytes per pixel, so images over
// maxAvatarPixels are refused before decoding (a 48 MP phone photo still fits).
const (
	maxAvatarSide        = 4096
	maxAvatarPixels      = 50_000_000
	maxFFmpegOutputBytes = 80 << 20
)

var errAvatarTooLarge = errors.New("image is too large")

// convertAvatarToJPEG turns any image into the JPEG LINE expects for group
// photos. The extension only uploads JPEG and flattens animated GIFs to their
// first frame, so the same is done here. JPEGs that fit are sent unchanged to
// keep their quality and EXIF orientation.
func convertAvatarToJPEG(ctx context.Context, data []byte) ([]byte, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		// HEIC, AVIF, TIFF and animated WebP have no Go decoder here.
		data, err = convertFirstFrameWithFFmpeg(ctx, data)
		if err != nil {
			return nil, fmt.Errorf("unsupported avatar image: %w", err)
		}
		cfg, format, err = image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("failed to read ffmpeg output: %w", err)
		}
	}
	if cfg.Width*cfg.Height > maxAvatarPixels {
		return nil, fmt.Errorf("%w: %dx%d", errAvatarTooLarge, cfg.Width, cfg.Height)
	}
	if format == "jpeg" && cfg.Width <= maxAvatarSide && cfg.Height <= maxAvatarSide {
		return data, nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to decode avatar: %w", err)
	}
	return encodeAvatarJPEG(img)
}

func fitAvatarSize(width, height int) (int, int) {
	if width <= maxAvatarSide && height <= maxAvatarSide {
		return width, height
	}
	if width >= height {
		return maxAvatarSide, max(1, height*maxAvatarSide/width)
	}
	return max(1, width*maxAvatarSide/height), maxAvatarSide
}

func encodeAvatarJPEG(img image.Image) ([]byte, error) {
	// JPEG has no alpha channel, so draw transparent images on white.
	bounds := img.Bounds()
	width, height := fitAvatarSize(bounds.Dx(), bounds.Dy())
	flat := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(flat, flat.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	if width == bounds.Dx() && height == bounds.Dy() {
		draw.Draw(flat, flat.Bounds(), img, bounds.Min, draw.Over)
	} else {
		draw.CatmullRom.Scale(flat, flat.Bounds(), img, bounds, draw.Over, nil)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, flat, &jpeg.Options{Quality: avatarJPEGQuality}); err != nil {
		return nil, fmt.Errorf("failed to encode avatar JPEG: %w", err)
	}
	return buf.Bytes(), nil
}

// cappedBuffer fails writes past max bytes, so a misbehaving ffmpeg can't fill memory.
type cappedBuffer struct {
	bytes.Buffer
	max int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.max {
		return 0, errAvatarTooLarge
	}
	return b.Buffer.Write(p)
}

// convertFirstFrameWithFFmpeg returns the first frame as a PNG that fits in
// maxAvatarSide. PNG keeps transparency so encodeAvatarJPEG can flatten it.
func convertFirstFrameWithFFmpeg(ctx context.Context, data []byte) ([]byte, error) {
	input, err := os.CreateTemp("", "line-avatar-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temporary avatar file: %w", err)
	}
	inputPath := input.Name()
	defer os.Remove(inputPath)
	if _, err = input.Write(data); err != nil {
		input.Close()
		return nil, fmt.Errorf("failed to write temporary avatar file: %w", err)
	}
	if err = input.Close(); err != nil {
		return nil, fmt.Errorf("failed to close temporary avatar file: %w", err)
	}

	output := &cappedBuffer{max: maxFFmpegOutputBytes}
	scale := fmt.Sprintf("scale='min(%[1]d,iw)':'min(%[1]d,ih)':force_original_aspect_ratio=decrease", maxAvatarSide)
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error",
		"-i", inputPath,
		"-frames:v", "1", "-vf", scale,
		"-f", "image2pipe", "-vcodec", "png", "pipe:1",
	)
	cmd.Stdout = output
	if err = cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("ffmpeg failed: %w", ctxErr)
		}
		return nil, fmt.Errorf("ffmpeg failed: %w", err)
	}
	return output.Bytes(), nil
}
