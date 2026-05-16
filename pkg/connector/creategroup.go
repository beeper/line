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

	// Newly created LINE groups keep invited users pending until they accept.
	// Group-key registration must match the active LINE member list, so cache
	// only our own MID here.
	lc.cacheMu.Lock()
	if lc.groupMemberCache == nil {
		lc.groupMemberCache = make(map[string][]string)
	}
	lc.groupMemberCache[chat.ChatMid] = []string{lc.Mid}
	lc.cacheMu.Unlock()

	// Register E2EE group key so messages can be sent while invitees are still pending.
	// This is best-effort: if registration fails we log a warning without aborting.
	if lc.E2EE != nil && lc.Mid != "" {
		if err := lc.registerGroupKey(ctx, chat.ChatMid, []string{lc.Mid}); err != nil {
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

// registerGroupKey generates a random 32-byte group key, wraps it for each member
// using ECDH + AES-256-CBC, and registers it with the LINE server so all members
// can decrypt group messages.
func (lc *LineClient) registerGroupKey(ctx context.Context, chatMid string, members []string) error {
	if lc.E2EE == nil {
		return fmt.Errorf("E2EE manager not initialized")
	}
	myRawKeyID, myPublicKey, err := lc.E2EE.MyPublicKey()
	if err != nil {
		return fmt.Errorf("missing own E2EE key: %w", err)
	}

	seen := make(map[string]struct{}, len(members)+1)
	normalizedMembers := make([]string, 0, len(members)+1)
	for _, mid := range append(members, lc.Mid) {
		if mid == "" {
			continue
		}
		lowerMid := strings.ToLower(mid)
		if strings.HasPrefix(lowerMid, "c") || strings.HasPrefix(lowerMid, "r") {
			continue
		}
		if _, ok := seen[mid]; ok {
			continue
		}
		seen[mid] = struct{}{}
		normalizedMembers = append(normalizedMembers, mid)
	}
	if len(normalizedMembers) == 0 {
		return fmt.Errorf("no members to register group key for")
	}
	members = normalizedMembers

	client := line.NewClient(lc.AccessToken)

	// Fetch current E2EE public keys for all other members as a batch. If the batch
	// call fails (e.g. server 500 for a specific member), fall back to fetching
	// each member's key individually via NegotiateE2EEPublicKey.
	peerMembers := make([]string, 0, len(members))
	for _, mid := range members {
		if mid != lc.Mid {
			peerMembers = append(peerMembers, mid)
		}
	}
	pubKeys := map[string]line.E2EEPeerPublicKey{
		lc.Mid: {KeyID: myRawKeyID, KeyData: myPublicKey},
	}
	pubKeysReq := line.GetLastE2EEPublicKeysRequest{
		ChatMid: chatMid,
		Members: peerMembers,
	}
	if len(peerMembers) > 0 {
		peerPubKeys, err := client.GetLastE2EEPublicKeys(pubKeysReq)
		if err != nil {
			if lc.isRefreshRequired(err) || lc.isLoggedOut(err) {
				if errRecover := lc.recoverToken(ctx); errRecover == nil {
					client = line.NewClient(lc.AccessToken)
					peerPubKeys, err = client.GetLastE2EEPublicKeys(pubKeysReq)
				}
			}
		}
		if err == nil {
			for mid, pk := range peerPubKeys {
				pubKeys[mid] = pk
			}
		} else {
			// Batch call failed — try individual key negotiation per member
			lc.UserLogin.Bridge.Log.Warn().Err(err).
				Str("chat_mid", chatMid).
				Int("members", len(peerMembers)).
				Msg("Batch GetLastE2EEPublicKeys failed, falling back to per-member NegotiateE2EEPublicKey")
			for _, mid := range peerMembers {
				res, nErr := client.NegotiateE2EEPublicKey(mid)
				if nErr != nil {
					if line.IsNoUsableE2EEPublicKey(nErr) {
						lc.UserLogin.Bridge.Log.Debug().Str("member", mid).Msg("Member has Letter Sealing disabled, skipping")
						continue
					}
					if lc.isRefreshRequired(nErr) || lc.isLoggedOut(nErr) {
						if errRecover := lc.recoverToken(ctx); errRecover == nil {
							client = line.NewClient(lc.AccessToken)
							res, nErr = client.NegotiateE2EEPublicKey(mid)
						}
					}
					if nErr != nil {
						lc.UserLogin.Bridge.Log.Warn().Err(nErr).Str("member", mid).Msg("Failed to negotiate key for member, skipping")
						continue
					}
				}
				keyID, nErr := res.KeyID.Int64()
				if nErr != nil {
					lc.UserLogin.Bridge.Log.Warn().Err(nErr).Str("member", mid).Msg("Failed to parse key ID, skipping")
					continue
				}
				pubKeys[mid] = line.E2EEPeerPublicKey{KeyID: int(keyID), KeyData: res.PublicKey}
			}
		}
	}

	// Generate group key in WASM (same approach as LINE Chrome Extension).
	// The generated key is a Curve25519Key object stored in the WASM module.
	groupKeyID, err := lc.E2EE.GenerateGroupKey()
	if err != nil {
		return fmt.Errorf("failed to generate group key: %w", err)
	}

	// LINE expects the registration member list to match the chat member list.
	// Members without usable E2EE keys stay in the payload with empty key slots.
	apiMembers := make([]string, 0, len(members))
	keyIds := make([]int, 0, len(members))
	encryptedKeys := make([]string, 0, len(members))

	validKeyCount := 0
	for _, mid := range members {
		apiMembers = append(apiMembers, mid)

		pk, ok := pubKeys[mid]
		if !ok {
			lc.UserLogin.Bridge.Log.Debug().Str("member", mid).Msg("No E2EE public key for member, registering empty key slot")
			keyIds = append(keyIds, 0)
			encryptedKeys = append(encryptedKeys, "")
			continue
		}
		if pk.KeyData == "" {
			lc.UserLogin.Bridge.Log.Debug().Str("member", mid).Int("key_id", pk.KeyID).Msg("Empty public key data for member, registering empty key slot")
			keyIds = append(keyIds, pk.KeyID)
			encryptedKeys = append(encryptedKeys, "")
			continue
		}

		encryptedKey, err := lc.E2EE.WrapGroupKeyForMember(pk.KeyData, groupKeyID)
		if err != nil {
			lc.UserLogin.Bridge.Log.Warn().Err(err).Str("member", mid).Msg("Failed to wrap group key for member, registering empty key slot")
			keyIds = append(keyIds, pk.KeyID)
			encryptedKeys = append(encryptedKeys, "")
			continue
		}

		keyIds = append(keyIds, pk.KeyID)
		encryptedKeys = append(encryptedKeys, encryptedKey)
		validKeyCount++
	}

	if validKeyCount == 0 {
		return fmt.Errorf("no members with valid E2EE keys")
	}

	if err := client.RegisterE2EEGroupKey(1, chatMid, apiMembers, keyIds, encryptedKeys); err != nil {
		if lc.isRefreshRequired(err) || lc.isLoggedOut(err) {
			if errRecover := lc.recoverToken(ctx); errRecover == nil {
				client = line.NewClient(lc.AccessToken)
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
