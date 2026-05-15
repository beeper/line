package connector

import (
	"context"
	"fmt"
	"strings"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

var _ bridgev2.GroupCreatingNetworkAPI = (*LineClient)(nil)

func (lc *LineClient) CreateGroup(ctx context.Context, params *bridgev2.GroupCreateParams) (*bridgev2.CreateChatResponse, error) {
	participantMids := make([]string, len(params.Participants))
	for i, p := range params.Participants {
		participantMids[i] = string(p)
	}

	name := ""
	if params.Name != nil {
		name = params.Name.Name
	}

	client := line.NewClient(lc.AccessToken)
	var chat *line.Chat
	var err error
	chatType := 0 // GROUP
	switch params.Type {
	case "room":
		chatType = 1 // ROOM
	}
	chat, err = client.CreateChat(participantMids, name, chatType)
	if err != nil && (lc.isRefreshRequired(err) || lc.isLoggedOut(err)) {
		if errRecover := lc.recoverToken(ctx); errRecover == nil {
			client = line.NewClient(lc.AccessToken)
			chat, err = client.CreateChat(participantMids, name, chatType)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("failed to create LINE chat: %w", err)
	}

	lc.UserLogin.Bridge.Log.Info().
		Str("chat_mid", chat.ChatMid).
		Str("name", chat.ChatName).
		Int("participants", len(participantMids)).
		Msg("LINE group chat created")

	portalKey := networkid.PortalKey{
		ID:       makePortalID(chat.ChatMid),
		Receiver: lc.UserLogin.ID,
	}

	portal, err := lc.UserLogin.Bridge.GetPortalByKey(ctx, portalKey)
	if err != nil {
		return nil, fmt.Errorf("failed to get portal for new chat: %w", err)
	}

	members := make([]bridgev2.ChatMember, 0, len(participantMids)+1)
	members = append(members, bridgev2.ChatMember{
		EventSender: bridgev2.EventSender{
			IsFromMe: true,
			Sender:   networkid.UserID(lc.UserLogin.ID),
		},
		Membership: event.MembershipJoin,
	})

	for _, mid := range participantMids {
		if mid == lc.Mid || mid == string(lc.UserLogin.ID) {
			continue
		}
		lowerMid := strings.ToLower(mid)
		if strings.HasPrefix(lowerMid, "c") || strings.HasPrefix(lowerMid, "r") {
			continue
		}
		members = append(members, bridgev2.ChatMember{
			EventSender: bridgev2.EventSender{
				Sender: makeUserID(mid),
			},
			Membership: event.MembershipJoin,
		})
	}

	ct := database.RoomTypeGroupDM
	chatName := chat.ChatName
	if chatName == "" {
		chatName = name
	}

	return &bridgev2.CreateChatResponse{
		PortalKey: portalKey,
		Portal:    portal,
		PortalInfo: &bridgev2.ChatInfo{
			Type: &ct,
			Name: &chatName,
			Members: &bridgev2.ChatMemberList{
				IsFull:  true,
				Members: members,
			},
		},
	}, nil
}
