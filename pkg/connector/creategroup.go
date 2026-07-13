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

var (
	_ bridgev2.GroupCreatingNetworkAPI = (*LineClient)(nil)

	getLastE2EEPublicKeysWithClient = func(client *line.Client, req line.GetLastE2EEPublicKeysRequest) (map[string]line.E2EEPeerPublicKey, error) {
		return client.GetLastE2EEPublicKeys(req)
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

	client := lc.newClient()
	var chat *line.Chat
	var err error
	chatType := 1 // ROOM: members join automatically.
	lineName := name
	chat, err = client.CreateChat(participantMids, lineName, chatType)
	if err != nil && lc.shouldAttemptTokenRecovery(ctx, err) {
		if errRecover := lc.recoverToken(ctx); errRecover == nil {
			client = lc.newClient()
			chat, err = client.CreateChat(participantMids, lineName, chatType)
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

	// Cache the member list so auto-registration can fall back to it
	// when GetChats withMembers returns empty data.
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
		if err := lc.registerGroupKey(ctx, chat.ChatMid, participantMids); err != nil {
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
				IsFull:  true,
				Members: members,
			},
		},
	}, nil
}

func (lc *LineClient) groupKeyMemberMIDs(chatMid string, members []string) []string {
	otherMembers := make([]string, 0, len(members))
	seen := make(map[string]struct{}, len(members))
	for _, mid := range members {
		if !isUserMID(mid) || lc.isOwnMID(mid) || mid == chatMid {
			continue
		}
		if _, ok := seen[mid]; ok {
			continue
		}
		seen[mid] = struct{}{}
		otherMembers = append(otherMembers, mid)
	}
	return otherMembers
}

func (lc *LineClient) resolveGroupMemberPublicKeys(ctx context.Context, client *line.Client, chatMid string, members []string) (*line.Client, map[string]line.E2EEPeerPublicKey, error) {
	req := line.GetLastE2EEPublicKeysRequest{ChatMid: chatMid, Members: members}
	client, pubKeys, batchErr := callLineResultUsing(lc, ctx, client, func(client *line.Client) (map[string]line.E2EEPeerPublicKey, error) {
		return getLastE2EEPublicKeysWithClient(client, req)
	})
	if batchErr != nil {
		if ctx.Err() != nil || lc.isSessionInvalidated() || line.IsAuthError(batchErr) {
			return client, nil, batchErr
		}
		lc.UserLogin.Bridge.Log.Warn().Err(batchErr).
			Str("chat_mid", chatMid).
			Int("members", len(members)).
			Msg("Batch GetLastE2EEPublicKeys failed, resolving member keys individually")
	}
	if pubKeys == nil {
		pubKeys = make(map[string]line.E2EEPeerPublicKey, len(members))
	}

	for _, mid := range members {
		if pk, ok := pubKeys[mid]; ok && pk.KeyID > 0 && pk.KeyData != "" {
			continue
		}

		var res *line.E2EEPublicKey
		var err error
		client, res, err = callLineResultUsing(lc, ctx, client, func(client *line.Client) (*line.E2EEPublicKey, error) {
			return negotiateE2EEPublicKeyWithClient(client, mid)
		})
		if err != nil {
			if ctx.Err() != nil || lc.isSessionInvalidated() || line.IsAuthError(err) {
				return client, nil, fmt.Errorf("negotiate E2EE key for member %s: %w", mid, err)
			}
			if line.IsNoUsableE2EEPublicKey(err) {
				return client, nil, fmt.Errorf("%w: member %s has Letter Sealing disabled", line.ErrNoUsableE2EEGroupKey, mid)
			}
			return client, nil, fmt.Errorf("%w: negotiate E2EE key for member %s: %w", line.ErrNoUsableE2EEGroupKey, mid, err)
		}
		if res == nil || res.PublicKey == "" {
			return client, nil, fmt.Errorf("%w: member %s returned no public key", line.ErrNoUsableE2EEGroupKey, mid)
		}
		keyID, err := res.KeyID.Int64()
		if err != nil {
			return client, nil, fmt.Errorf("%w: parse E2EE key ID for member %s: %w", line.ErrNoUsableE2EEGroupKey, mid, err)
		}
		if keyID <= 0 {
			return client, nil, fmt.Errorf("%w: member %s returned invalid key ID %d", line.ErrNoUsableE2EEGroupKey, mid, keyID)
		}
		pubKeys[mid] = line.E2EEPeerPublicKey{KeyID: int(keyID), KeyData: res.PublicKey}
	}

	return client, pubKeys, nil
}

// registerGroupKey generates a random 32-byte group key, wraps it for each member
// using ECDH + AES-256-CBC, and registers it with the LINE server so all members
// can decrypt group messages.
func (lc *LineClient) registerGroupKey(ctx context.Context, chatMid string, members []string) error {
	members = lc.groupKeyMemberMIDs(chatMid, members)
	if lc.E2EE == nil {
		return fmt.Errorf("%w: E2EE manager not initialized", line.ErrNoUsableE2EEGroupKey)
	}

	client := lc.newClient()

	// Batch responses can be partial without returning an error. Resolve every
	// missing member individually so registration arrays retain the server's
	// expected member count.
	pubKeys := make(map[string]line.E2EEPeerPublicKey, len(members))
	if len(members) > 0 {
		var err error
		client, pubKeys, err = lc.resolveGroupMemberPublicKeys(ctx, client, chatMid, members)
		if err != nil {
			return err
		}
	}

	// Generate group key in WASM (same approach as LINE Chrome Extension).
	// The generated key is a Curve25519Key object stored in the WASM module.
	groupKeyID, err := lc.E2EE.GenerateGroupKey()
	if err != nil {
		return fmt.Errorf("failed to generate group key: %w", err)
	}

	// Wrap the group key for each member that has a public key
	apiMembers := make([]string, 0, len(members))
	keyIds := make([]int, 0, len(members))
	encryptedKeys := make([]string, 0, len(members))

	for _, mid := range members {
		pk, ok := pubKeys[mid]
		if !ok || pk.KeyID <= 0 || pk.KeyData == "" {
			return fmt.Errorf("%w: incomplete E2EE public key for member %s", line.ErrNoUsableE2EEGroupKey, mid)
		}

		encryptedKey, err := lc.E2EE.WrapGroupKeyForMember(pk.KeyData, groupKeyID)
		if err != nil {
			return fmt.Errorf("wrap group key for member %s: %w", mid, err)
		}

		apiMembers = append(apiMembers, mid)
		keyIds = append(keyIds, pk.KeyID)
		encryptedKeys = append(encryptedKeys, encryptedKey)
	}

	// LINE's registerE2EEGroupKey requires the caller's own key entry as well — without it the
	// server rejects the request with "empty caller key". The Chrome extension wraps the group
	// key for every member returned by getLastE2EEPublicKeys, which includes the caller. Mirror
	// that by wrapping the group key for our own public key and appending ourselves.
	selfRawID, selfPub, err := lc.E2EE.MyPublicKey()
	if err != nil {
		return fmt.Errorf("get own E2EE key: %w", err)
	}
	selfEncryptedKey, err := lc.E2EE.WrapGroupKeyForMember(selfPub, groupKeyID)
	if err != nil {
		return fmt.Errorf("wrap group key for self: %w", err)
	}
	apiMembers = append(apiMembers, lc.Mid)
	keyIds = append(keyIds, selfRawID)
	encryptedKeys = append(encryptedKeys, selfEncryptedKey)

	if err := client.RegisterE2EEGroupKey(1, chatMid, apiMembers, keyIds, encryptedKeys); err != nil {
		if lc.shouldAttemptTokenRecovery(ctx, err) {
			if errRecover := lc.recoverToken(ctx); errRecover == nil {
				client = lc.newClient()
				err = client.RegisterE2EEGroupKey(1, chatMid, apiMembers, keyIds, encryptedKeys)
			}
		}
		if err != nil {
			return fmt.Errorf("registerE2EEGroupKey failed: %w", err)
		}
	}

	lc.UserLogin.Bridge.Log.Info().
		Str("chat_mid", chatMid).
		Int("members", len(apiMembers)).
		Msg("Registered E2EE group key")

	return nil
}
