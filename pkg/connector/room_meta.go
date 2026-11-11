package connector

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"strings"
	"unicode/utf16"

	"go.mau.fi/util/ffmpeg"
	_ "golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"github.com/beeper/line/pkg/line"
)

var (
	_ bridgev2.RoomNameHandlingNetworkAPI   = (*LineClient)(nil)
	_ bridgev2.RoomAvatarHandlingNetworkAPI = (*LineClient)(nil)
)

const (
	maxLineGroupNameLength = 50
	maxLineGroupPictureDim = 1280
)

var (
	errGroupNameEmpty   = errors.New("LINE group names can't be empty")
	errGroupNameTooLong = fmt.Errorf("LINE group names can be at most %d characters", maxLineGroupNameLength)
	errNotEditableChat  = errors.New("LINE doesn't support changing this chat's name or photo")
)

func isEditableGroupMID(mid string) bool {
	return isChatMID(mid) && !strings.HasPrefix(mid, "R")
}

func lineGroupPowerLevels() *bridgev2.PowerLevelOverrides {
	return &bridgev2.PowerLevelOverrides{
		Events: map[event.Type]int{
			event.StateRoomName:   0,
			event.StateRoomAvatar: 0,
		},
	}
}

func (lc *LineClient) updateChatAttribute(ctx context.Context, chatMid string, attribute int, field string, value any) error {
	_, err := lc.callLine(ctx, func(client *line.Client) error {
		chat, err := client.GetChatRaw(ctx, chatMid)
		if err != nil {
			return err
		}
		chat[field] = value
		return client.UpdateChat(ctx, int64(lc.nextUntrackedReqSeq()), chat, attribute)
	})
	return err
}

func (lc *LineClient) HandleMatrixRoomName(ctx context.Context, msg *bridgev2.MatrixRoomName) (bool, error) {
	chatMid := string(msg.Portal.ID)
	if !isEditableGroupMID(chatMid) {
		return false, errNotEditableChat
	}
	name := strings.TrimSpace(msg.Content.Name)
	if name == "" {
		return false, errGroupNameEmpty
	} else if len(utf16.Encode([]rune(name))) > maxLineGroupNameLength {
		return false, errGroupNameTooLong
	}
	if err := lc.updateChatAttribute(ctx, chatMid, line.ChatAttributeName, "chatName", name); err != nil {
		return false, wrapSendError(err)
	}
	msg.Portal.Name = name
	msg.Portal.NameSet = true
	return true, nil
}

func (lc *LineClient) HandleMatrixRoomAvatar(ctx context.Context, msg *bridgev2.MatrixRoomAvatar) (bool, error) {
	chatMid := string(msg.Portal.ID)
	if !isEditableGroupMID(chatMid) {
		return false, errNotEditableChat
	}
	if msg.Content.URL == "" && msg.Content.MSC3414File == nil {
		if err := lc.updateChatAttribute(ctx, chatMid, line.ChatAttributePictureStatus, "picturePath", ""); err != nil {
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
		return false, fmt.Errorf("failed to download avatar: %w", err)
	}
	var mimeType string
	if msg.Content.Info != nil {
		mimeType = msg.Content.Info.MimeType
	}
	jpegData, err := convertGroupPicture(ctx, data, mimeType)
	if err != nil {
		return false, fmt.Errorf("failed to convert avatar: %w", err)
	}
	var picturePath string
	_, err = lc.callLine(ctx, func(client *line.Client) error {
		if err := client.UploadChatPicture(ctx, chatMid, jpegData, "image/jpeg"); err != nil {
			return err
		}
		chats, err := client.GetChats([]string{chatMid}, false, false)
		if err == nil && len(chats.Chats) > 0 {
			picturePath = chats.Chats[0].PicturePath
		}
		return nil
	})
	if err != nil {
		return false, wrapSendError(err)
	}
	msg.Portal.AvatarID = networkid.AvatarID(picturePath)
	msg.Portal.AvatarMXC = avatarURL
	msg.Portal.AvatarHash = sha256.Sum256(data)
	msg.Portal.AvatarSet = true
	return true, nil
}

func convertGroupPicture(ctx context.Context, data []byte, mimeType string) ([]byte, error) {
	if isHEICImage(mimeType, "") {
		converted, err := convertHEICToJPEG(ctx, data)
		if err != nil {
			return nil, err
		}
		data = converted
	}
	if cfg, format, err := image.DecodeConfig(bytes.NewReader(data)); err == nil && format == "jpeg" &&
		cfg.Width <= maxLineGroupPictureDim && cfg.Height <= maxLineGroupPictureDim {
		return data, nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		if !ffmpeg.Supported() {
			return nil, err
		}
		png, convErr := ffmpeg.ConvertBytes(ctx, data, ".png", nil, []string{"-frames:v", "1"}, mimeType)
		if convErr != nil {
			return nil, fmt.Errorf("%w (ffmpeg: %w)", err, convErr)
		}
		if img, _, err = image.Decode(bytes.NewReader(png)); err != nil {
			return nil, err
		}
	}
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= 0 || height <= 0 {
		return nil, errors.New("image has no pixels")
	}
	if width > maxLineGroupPictureDim || height > maxLineGroupPictureDim {
		if width >= height {
			width, height = maxLineGroupPictureDim, max(1, height*maxLineGroupPictureDim/width)
		} else {
			width, height = max(1, width*maxLineGroupPictureDim/height), maxLineGroupPictureDim
		}
	}
	flat := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(flat, flat.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.CatmullRom.Scale(flat, flat.Bounds(), img, bounds, draw.Over, nil)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, flat, &jpeg.Options{Quality: 90}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
