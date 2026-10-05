package connector

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

var (
	_ bridgev2.GroupCreatingNetworkAPI = (*LineClient)(nil)

	getLastE2EEPublicKeysWithClient = func(client *line.Client, chatMid string) (map[string]line.E2EEPeerPublicKey, error) {
		return client.GetLastE2EEPublicKeys(chatMid)
	}
)

func (lc *LineClient) CreateGroup(ctx context.Context, params *bridgev2.GroupCreateParams) (*bridgev2.CreateChatResponse, error) {
	participantMids := make([]string, len(params.Participants))
	for i, p := range params.Participants {
		participantMids[i] = string(p)
	}

	name := ""
	if params.Name != nil {
		name = params.Name.Name
	}

	chatType := 1 // ROOM: members join automatically.
	lineName := name
	_, chat, err := callLineResult(lc, ctx, func(client *line.Client) (*line.Chat, error) {
		return client.CreateChat(participantMids, lineName, chatType)
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create LINE chat: %w", err)
	}

	lc.UserLogin.Bridge.Log.Info().
		Str("chat_mid", chat.ChatMid).
		Str("name", chat.ChatName).
		Int("participants", len(participantMids)).
		Msg("LINE group chat created")

	// Cache the member list for group name generation.
	groupMembers := make([]string, 0, len(participantMids)+1)
	groupMembers = append(groupMembers, lc.Mid)
	groupMembers = append(groupMembers, participantMids...)
	lc.cacheMu.Lock()
	if lc.groupMemberCache == nil {
		lc.groupMemberCache = make(map[string][]string)
	}
	if lc.generatedGroupNameCache == nil {
		lc.generatedGroupNameCache = make(map[string]bool)
	}
	lc.groupMemberCache[chat.ChatMid] = groupMembers
	lc.generatedGroupNameCache[chat.ChatMid] = name == ""
	lc.cacheMu.Unlock()

	// Registration is best-effort: an incomplete E2EE member list leaves the
	// chat on its plaintext fallback without aborting group creation.
	if lc.E2EE != nil && len(participantMids) > 0 {
		if err := lc.registerGroupKey(ctx, chat.ChatMid); err != nil {
			lc.UserLogin.Bridge.Log.Warn().Err(err).
				Str("chat_mid", chat.ChatMid).
				Msg("Failed to register E2EE group key, continuing without E2EE")
		}
	}

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
	chatName := name
	if chatName == "" {
		chatName = lc.generateNameFromMemberList(ctx, groupMembers)
	}
	if chatName == "" {
		chatName = chat.ChatName
	}

	return &bridgev2.CreateChatResponse{
		PortalKey: portalKey,
		Portal:    portal,
		PortalInfo: &bridgev2.ChatInfo{
			Type: &ct,
			Name: &chatName,
			Members: &bridgev2.ChatMemberList{
				IsFull:    true,
				MemberMap: chatMemberMapFromList(members),
			},
		},
	}, nil
}

type groupKeyCrypto interface {
	GenerateGroupKey() (int, error)
	WrapGroupKeyForMember(string, int) (string, error)
}

type groupKeyReleaser interface {
	ReleaseGroupKey(int) error
}

func wrapRegistrationGroupKey(ctx context.Context, crypto groupKeyCrypto, pubKeys map[string]line.E2EEPeerPublicKey) (apiMembers []string, keyIDs []int, encryptedKeys []string, err error) {
	groupKeyID, err := crypto.GenerateGroupKey()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to generate group key: %w", err)
	}
	if releaser, ok := crypto.(groupKeyReleaser); ok {
		defer func() {
			if recovered := recover(); recovered != nil {
				panic(recovered)
			}
			if releaseErr := releaser.ReleaseGroupKey(groupKeyID); releaseErr != nil {
				err = errors.Join(err, fmt.Errorf("release generated group key: %w", releaseErr))
			}
		}()
	}
	apiMembers = make([]string, 0, len(pubKeys))
	keyIDs = make([]int, 0, len(pubKeys))
	encryptedKeys = make([]string, 0, len(pubKeys))
	for mid, pk := range pubKeys {
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, err
		}
		encryptedKey, err := crypto.WrapGroupKeyForMember(pk.KeyData, groupKeyID)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("wrap group key for member: %w", err)
		}
		apiMembers = append(apiMembers, mid)
		keyIDs = append(keyIDs, pk.KeyID)
		encryptedKeys = append(encryptedKeys, encryptedKey)
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, nil, err
	}
	return apiMembers, keyIDs, encryptedKeys, nil
}

func (lc *LineClient) registerGroupKey(ctx context.Context, chatMid string) error {
	if lc.E2EE == nil {
		return fmt.Errorf("%w: E2EE manager not initialized", line.ErrNoUsableE2EEGroupKey)
	}
	return lc.registerGroupKeyWithCrypto(ctx, chatMid, lc.E2EE)
}

func (lc *LineClient) registerGroupKeyWithCrypto(ctx context.Context, chatMid string, crypto groupKeyCrypto) error {
	client := lc.newClient()
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		// LINE returns the current registration recipients, including the caller.
		// GetChats and Matrix membership can be incomplete or stale.
		var pubKeys map[string]line.E2EEPeerPublicKey
		var err error
		client, pubKeys, err = callLineResultUsing(lc, ctx, client, func(client *line.Client) (map[string]line.E2EEPeerPublicKey, error) {
			return getLastE2EEPublicKeysWithClient(client, chatMid)
		})
		if err != nil {
			return fmt.Errorf("getLastE2EEPublicKeys failed: %w", err)
		}
		if len(pubKeys) == 0 {
			return fmt.Errorf("%w: no registration members returned for group key", line.ErrNoUsableE2EEGroupKey)
		}
		if _, ok := pubKeys[lc.Mid]; !ok {
			return fmt.Errorf("%w: group key registration members do not include caller", line.ErrNoUsableE2EEGroupKey)
		}
		for mid, pk := range pubKeys {
			if !isUserMID(mid) {
				return fmt.Errorf("invalid E2EE group member MID")
			}
			if pk.KeyID <= 0 || pk.KeyData == "" {
				return fmt.Errorf("%w: incomplete E2EE group member public key", line.ErrNoUsableE2EEGroupKey)
			}
		}

		apiMembers, keyIDs, encryptedKeys, err := wrapRegistrationGroupKey(ctx, crypto, pubKeys)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		client, err = lc.callLineUsing(ctx, client, func(client *line.Client) error {
			return client.RegisterE2EEGroupKey(1, chatMid, apiMembers, keyIDs, encryptedKeys)
		})
		if attempt == 0 && line.IsE2EEGroupMemberMismatch(err) {
			lc.UserLogin.Bridge.Log.Debug().Int("members", len(pubKeys)).
				Msg("Group membership changed during key registration, refreshing member keys")
			continue
		}
		if err != nil {
			return fmt.Errorf("registerE2EEGroupKey failed: %w", err)
		}
		lc.UserLogin.Bridge.Log.Info().Str("chat_mid", chatMid).
			Int("members", len(apiMembers)).Msg("Registered E2EE group key")
		return nil
	}
}
