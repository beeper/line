package connector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

var (
	_ bridgev2.RoomNameHandlingNetworkAPI   = (*LineClient)(nil)
	_ bridgev2.RoomAvatarHandlingNetworkAPI = (*LineClient)(nil)
)

func (lc *LineClient) HandleMatrixRoomName(ctx context.Context, msg *bridgev2.MatrixRoomName) (bool, error) {
	chatMid, err := matrixGroupChatMID(msg.Portal)
	if err != nil {
		return false, err
	}

	name := msg.Content.Name
	if strings.TrimSpace(name) == "" {
		return false, fmt.Errorf("LINE group name cannot be empty")
	}
	if len([]rune(name)) > 50 {
		return false, fmt.Errorf("LINE group name cannot be longer than 50 characters")
	}

	err = lc.withLineClientRetry(ctx, func(client *line.Client) error {
		chat, err := client.GetChatForUpdate(chatMid)
		if err != nil {
			return err
		}
		chat["chatName"] = name
		return client.UpdateChat(chat, line.ChatAttributeName)
	})
	if err != nil {
		return false, err
	}

	msg.Portal.Name = name
	msg.Portal.NameSet = true
	return true, nil
}

func (lc *LineClient) HandleMatrixRoomAvatar(ctx context.Context, msg *bridgev2.MatrixRoomAvatar) (bool, error) {
	chatMid, err := matrixGroupChatMID(msg.Portal)
	if err != nil {
		return false, err
	}

	avatarURL := msg.Content.URL
	if avatarURL == "" && msg.Content.MSC3414File != nil {
		avatarURL = msg.Content.MSC3414File.URL
	}

	if avatarURL == "" {
		err = lc.withLineClientRetry(ctx, func(client *line.Client) error {
			chat, err := client.GetChatForUpdate(chatMid)
			if err != nil {
				return err
			}
			chat["picturePath"] = ""
			return client.UpdateChat(chat, line.ChatAttributePictureStatus)
		})
		if err != nil {
			return false, err
		}

		msg.Portal.AvatarMXC = ""
		msg.Portal.AvatarHash = [32]byte{}
		msg.Portal.AvatarID = "remove"
		msg.Portal.AvatarSet = true
		return true, nil
	}

	data, err := msg.Portal.Bridge.Bot.DownloadMedia(ctx, avatarURL, msg.Content.MSC3414File)
	if err != nil {
		return false, fmt.Errorf("failed to download Matrix room avatar: %w", err)
	}

	contentType := http.DetectContentType(data)
	if msg.Content.Info != nil && msg.Content.Info.MimeType != "" {
		contentType = msg.Content.Info.MimeType
	}
	if !strings.HasPrefix(contentType, "image/") {
		return false, fmt.Errorf("LINE group avatar must be an image, got %s", contentType)
	}

	err = lc.withLineClientRetry(ctx, func(client *line.Client) error {
		return client.UploadProfileImage(chatMid, data, contentType)
	})
	if err != nil {
		return false, err
	}

	hash := sha256.Sum256(data)
	msg.Portal.AvatarMXC = avatarURL
	msg.Portal.AvatarHash = hash
	msg.Portal.AvatarID = networkid.AvatarID(hex.EncodeToString(hash[:]))
	msg.Portal.AvatarSet = true
	return true, nil
}

func (lc *LineClient) withLineClientRetry(ctx context.Context, fn func(*line.Client) error) error {
	client := line.NewClient(lc.AccessToken)
	err := fn(client)
	if err != nil && (lc.isRefreshRequired(err) || lc.isLoggedOut(err)) {
		if errRecover := lc.recoverToken(ctx); errRecover == nil {
			client = line.NewClient(lc.AccessToken)
			err = fn(client)
		}
	}
	return err
}

func matrixGroupChatMID(portal *bridgev2.Portal) (string, error) {
	if portal == nil {
		return "", fmt.Errorf("portal is nil")
	}
	if portal.RoomType == database.RoomTypeDM {
		return "", fmt.Errorf("LINE does not support setting names or avatars for DMs")
	}
	chatMid := string(portal.ID)
	if !isLineGroupPortalID(chatMid) {
		return "", fmt.Errorf("LINE chat %s is not a group or room", chatMid)
	}
	return chatMid, nil
}

func isLineGroupPortalID(chatMid string) bool {
	chatMid = strings.ToLower(chatMid)
	return strings.HasPrefix(chatMid, "c") || strings.HasPrefix(chatMid, "r")
}
