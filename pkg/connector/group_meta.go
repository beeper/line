package connector

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"strings"
	"unicode/utf16"

	"github.com/rs/zerolog"
	_ "golang.org/x/image/bmp"
	"golang.org/x/image/draw"
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
	jpegData, err := convertAvatarToJPEG(data)
	if errors.Is(err, errAvatarFormat) || errors.Is(err, errAvatarTooLarge) {
		return false, err
	} else if err != nil {
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
	maxAvatarSide   = 4096
	maxAvatarPixels = 50_000_000
)

var (
	// The extension's file picker only allows these formats.
	errAvatarFormat   = groupMetaError(errors.New("LINE group photos must be JPEG, PNG, GIF or BMP"))
	errAvatarTooLarge = groupMetaError(fmt.Errorf("LINE group photos can be at most %d megapixels", maxAvatarPixels/1_000_000))
)

// convertAvatarToJPEG turns a JPEG, PNG, GIF or BMP into the JPEG LINE
// expects for group photos. The extension only uploads JPEG and flattens
// animated GIFs to their first frame, so the same is done here. JPEGs that fit
// are sent unchanged to keep their quality and EXIF orientation.
func convertAvatarToJPEG(data []byte) ([]byte, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, errAvatarFormat
	}
	switch format {
	case "jpeg", "png", "gif", "bmp":
	default:
		return nil, errAvatarFormat
	}
	if cfg.Width*cfg.Height > maxAvatarPixels {
		return nil, errAvatarTooLarge
	}
	if format == "jpeg" && cfg.Width <= maxAvatarSide && cfg.Height <= maxAvatarSide {
		return data, nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to decode avatar: %w", err)
	}
	orientation := 1
	if format == "jpeg" {
		orientation = jpegOrientation(data)
	}
	return encodeAvatarJPEG(img, orientation)
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

// encodeAvatarJPEG scales the image to fit, applies the EXIF orientation
// (re-encoding drops the EXIF data) and encodes it as JPEG.
func encodeAvatarJPEG(img image.Image, orientation int) ([]byte, error) {
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
	if err := jpeg.Encode(&buf, orientRGBA(flat, orientation), &jpeg.Options{Quality: avatarJPEGQuality}); err != nil {
		return nil, fmt.Errorf("failed to encode avatar JPEG: %w", err)
	}
	return buf.Bytes(), nil
}

// orientRGBA turns an image stored with the given EXIF orientation (1-8)
// upright.
func orientRGBA(src *image.RGBA, orientation int) *image.RGBA {
	if orientation < 2 || orientation > 8 {
		return src
	}
	w, h := src.Rect.Dx(), src.Rect.Dy()
	dw, dh := w, h
	if orientation >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		for x := 0; x < dw; x++ {
			var sx, sy int
			switch orientation {
			case 2: // mirrored
				sx, sy = w-1-x, y
			case 3: // rotated 180°
				sx, sy = w-1-x, h-1-y
			case 4: // mirrored vertically
				sx, sy = x, h-1-y
			case 5: // transposed
				sx, sy = y, x
			case 6: // needs a 90° clockwise turn
				sx, sy = y, h-1-x
			case 7: // transversed
				sx, sy = w-1-y, h-1-x
			case 8: // needs a 90° counter-clockwise turn
				sx, sy = w-1-y, x
			}
			copy(dst.Pix[dst.PixOffset(x, y):][:4], src.Pix[src.PixOffset(sx, sy):][:4])
		}
	}
	return dst
}

// jpegOrientation reads the EXIF orientation tag of a JPEG, or returns 1
// when there is none.
func jpegOrientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xff || data[1] != 0xd8 {
		return 1
	}
	for pos := 2; pos+4 <= len(data) && data[pos] == 0xff; {
		marker := data[pos+1]
		if marker == 0xda { // start of scan: no more metadata
			return 1
		}
		length := int(binary.BigEndian.Uint16(data[pos+2:]))
		if length < 2 || pos+2+length > len(data) {
			return 1
		}
		segment := data[pos+4 : pos+2+length]
		if marker == 0xe1 && bytes.HasPrefix(segment, []byte("Exif\x00\x00")) {
			return tiffOrientation(segment[6:])
		}
		pos += 2 + length
	}
	return 1
}

func tiffOrientation(tiff []byte) int {
	if len(tiff) < 8 {
		return 1
	}
	var order binary.ByteOrder
	switch string(tiff[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 1
	}
	ifd := int(order.Uint32(tiff[4:]))
	if ifd < 8 || ifd+2 > len(tiff) {
		return 1
	}
	count := int(order.Uint16(tiff[ifd:]))
	for i := 0; i < count; i++ {
		entry := ifd + 2 + i*12
		if entry+12 > len(tiff) {
			return 1
		}
		if order.Uint16(tiff[entry:]) == 0x0112 {
			if value := int(order.Uint16(tiff[entry+8:])); value >= 1 && value <= 8 {
				return value
			}
			return 1
		}
	}
	return 1
}
