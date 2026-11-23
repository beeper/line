package connector

import (
	"context"
	"errors"

	"maunium.net/go/mautrix/bridgev2"

	"github.com/beeper/line/pkg/line"
)

var _ bridgev2.MembershipHandlingNetworkAPI = (*LineClient)(nil)

var (
	errInviteToDM          = errors.New("can't add people to a LINE direct chat")
	errLegacyRoomMemberOps = errors.New("LINE doesn't support removing people from this chat")
	errNotLineUser         = errors.New("only LINE users can be added to or removed from LINE chats")
)

func (lc *LineClient) HandleMatrixMembership(ctx context.Context, msg *bridgev2.MatrixMembershipChange) (*bridgev2.MatrixMembershipResult, error) {
	if msg.Type.IsSelf && msg.OrigSender != nil {
		return nil, nil
	}
	chatMid := string(msg.Portal.ID)
	if !isChatMID(chatMid) {
		if msg.Type == bridgev2.Invite {
			return nil, errInviteToDM
		}
		return nil, nil
	}
	var targetMid string
	switch target := msg.Target.(type) {
	case *bridgev2.Ghost:
		targetMid = string(target.ID)
	case *bridgev2.UserLogin:
		targetMid = string(target.ID)
	}
	switch msg.Type {
	case bridgev2.Invite, bridgev2.Kick, bridgev2.RevokeInvite:
		if !isUserMID(targetMid) {
			return nil, errNotLineUser
		}
	}
	legacyRoom := isLegacyRoomMID(chatMid)
	reqSeq := int64(lc.nextUntrackedReqSeq())
	var call func(client *line.Client) error
	switch msg.Type {
	case bridgev2.Leave, bridgev2.RejectInvite:
		switch {
		case msg.Portal.MessageRequest:
			call = func(client *line.Client) error { return client.RejectChatInvitationContext(ctx, reqSeq, chatMid) }
		case legacyRoom:
			call = func(client *line.Client) error { return client.LeaveRoom(ctx, reqSeq, chatMid) }
		default:
			call = func(client *line.Client) error { return client.DeleteSelfFromChat(ctx, reqSeq, chatMid) }
		}
	case bridgev2.Invite:
		if legacyRoom {
			call = func(client *line.Client) error {
				return client.InviteIntoRoom(ctx, reqSeq, chatMid, []string{targetMid})
			}
		} else {
			call = func(client *line.Client) error {
				return client.InviteIntoChat(ctx, reqSeq, chatMid, []string{targetMid})
			}
		}
	case bridgev2.Kick:
		if legacyRoom {
			return nil, errLegacyRoomMemberOps
		}
		call = func(client *line.Client) error {
			return client.DeleteOtherFromChat(ctx, reqSeq, chatMid, []string{targetMid})
		}
	case bridgev2.RevokeInvite:
		if legacyRoom {
			return nil, errLegacyRoomMemberOps
		}
		call = func(client *line.Client) error {
			return client.CancelChatInvitation(ctx, reqSeq, chatMid, []string{targetMid})
		}
	default:
		return nil, nil
	}
	_, err := lc.callLine(ctx, call)
	return nil, wrapSendError(err)
}
