package connector

import (
	"context"
	"errors"
	"fmt"
	"time"

	"maunium.net/go/mautrix/bridgev2/networkid"

	"github.com/highesttt/matrix-line-messenger/pkg/e2ee"
	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

const noE2EETTL = 1 * time.Hour

var (
	negotiateE2EEPublicKeyWithClient = func(client *line.Client, mid string) (*line.E2EEPublicKey, error) {
		return client.NegotiateE2EEPublicKey(mid)
	}
	getE2EEPublicKeyWithClient = func(client *line.Client, mid string, keyVersion, keyID int) (*line.E2EEPublicKey, error) {
		return client.GetE2EEPublicKey(mid, keyVersion, keyID)
	}
)

func groupKeyFetchError(groupKeyID int, err error) error {
	if groupKeyID == 0 && line.IsNotAMemberError(err) {
		return fmt.Errorf("%w: latest group key lookup reported not a member: %w", line.ErrNoUsableE2EEGroupKey, err)
	}
	return err
}

// fetchAndUnwrapGroupKey retrieves a specific group key (or the latest when groupKeyID == 0)
// and unwraps it so the E2EE manager can encrypt/decrypt group messages.
// If no group key exists yet (TalkException code 5), it auto-registers one and retries.
func (lc *LineClient) fetchAndUnwrapGroupKey(ctx context.Context, chatMid string, groupKeyID int) error {
	if lc.E2EE == nil {
		return fmt.Errorf("E2EE manager not initialized")
	}

	client := lc.newClient()
	fetch := func() (*line.E2EEGroupSharedKey, error) {
		var sharedKey *line.E2EEGroupSharedKey
		var err error
		if groupKeyID > 0 {
			sharedKey, err = client.GetE2EEGroupSharedKey(chatMid, groupKeyID)
		} else {
			sharedKey, err = client.GetLastE2EEGroupSharedKey(chatMid)
		}
		return sharedKey, groupKeyFetchError(groupKeyID, err)
	}

	sharedKey, err := fetch()
	// No group key exists yet — auto-register one so the group can use E2EE.
	if err != nil && line.IsGroupKeyNotFound(err) {
		lc.UserLogin.Bridge.Log.Info().Str("chat_mid", chatMid).
			Msg("No group key found, auto-registering")
		if registerErr := lc.autoRegisterGroupKey(ctx, chatMid); registerErr != nil {
			lc.UserLogin.Bridge.Log.Warn().Err(registerErr).Str("chat_mid", chatMid).
				Msg("Auto-register group key failed")
			return fmt.Errorf("auto-register group key: %w", registerErr)
		}
		sharedKey, err = fetch()
	}
	// Token recovery for other error types
	if err != nil && !line.IsNoUsableE2EEGroupKey(err) && lc.shouldAttemptTokenRecovery(ctx, err) {
		if errRecover := lc.recoverToken(ctx); errRecover == nil {
			client = lc.newClient()
			sharedKey, err = fetch()
		} else {
			return fmt.Errorf("failed to recover token before fetching group key: %w", errRecover)
		}
	}
	if err != nil {
		return err
	}
	if sharedKey == nil {
		return fmt.Errorf("no group shared key returned for %s", chatMid)
	}

	unwrap := func(sharedKey *line.E2EEGroupSharedKey) error {
		lc.UserLogin.Bridge.Log.Debug().
			Str("chat_mid", chatMid).
			Int("group_key_id", sharedKey.GroupKeyID).
			Int("creator_key_id", sharedKey.CreatorKeyID).
			Int("receiver_key_id", sharedKey.ReceiverKeyID).
			Msg("Fetched group shared key")

		if _, _, err := lc.ensurePeerKey(ctx, sharedKey.Creator); err != nil {
			return fmt.Errorf("failed to ensure creator key: %w", err)
		}
		if _, _, err := lc.ensurePeerKeyByID(ctx, sharedKey.Creator, sharedKey.CreatorKeyID); err != nil {
			return fmt.Errorf("failed to ensure creator key id %d: %w", sharedKey.CreatorKeyID, err)
		}

		unwrappedID, err := lc.E2EE.UnwrapGroupSharedKey(chatMid, sharedKey)
		if err != nil {
			return fmt.Errorf("failed to unwrap group key: %w", err)
		}

		lc.UserLogin.Bridge.Log.Debug().
			Str("chat_mid", chatMid).
			Int("group_key_id", sharedKey.GroupKeyID).
			Int("receiver_key_id", sharedKey.ReceiverKeyID).
			Int("unwrapped_id", unwrappedID).
			Msg("Unwrapped group shared key")

		return nil
	}

	err = unwrap(sharedKey)
	if groupKeyID == 0 && errors.Is(err, e2ee.ErrMissingOwnPrivateKey) {
		lc.UserLogin.Bridge.Log.Warn().Err(err).Str("chat_mid", chatMid).
			Msg("Latest group key targets a missing private key, registering a fresh group key")
		if registerErr := lc.autoRegisterGroupKey(ctx, chatMid); registerErr != nil {
			return fmt.Errorf("%w (fresh group key registration failed: %v)", err, registerErr)
		}
		sharedKey, err = fetch()
		if err != nil {
			return fmt.Errorf("failed to fetch fresh group key after registration: %w", err)
		}
		if sharedKey == nil {
			return fmt.Errorf("no fresh group shared key returned for %s", chatMid)
		}
		err = unwrap(sharedKey)
	}

	return err
}

func (lc *LineClient) ensurePeerKey(ctx context.Context, mid string) (int, string, error) {
	lc.cacheMu.Lock()
	if lc.peerKeys == nil {
		lc.peerKeys = make(map[string]peerKeyInfo)
	}
	cached, ok := lc.peerKeys[mid]
	lc.cacheMu.Unlock()
	if ok {
		// Cached as Letter Sealing off — return error unless TTL expired
		if cached.noE2EE {
			if time.Since(cached.checkedAt) < noE2EETTL {
				return 0, "", line.ErrNoUsableE2EEPublicKey
			}
			// TTL expired, re-negotiate below
		} else if cached.raw != 0 && cached.pub != "" {
			if lc.E2EE != nil {
				lc.E2EE.RegisterPeerPublicKey(cached.raw, cached.pub)
			}
			return cached.raw, cached.pub, nil
		}
	}
	_, res, err := callLineResult(lc, ctx, func(client *line.Client) (*line.E2EEPublicKey, error) {
		return negotiateE2EEPublicKeyWithClient(client, mid)
	})
	if err != nil {
		// Cache negative result so we don't keep hitting the API
		if line.IsNoUsableE2EEPublicKey(err) {
			lc.cacheMu.Lock()
			lc.peerKeys[mid] = peerKeyInfo{noE2EE: true, checkedAt: time.Now()}
			lc.cacheMu.Unlock()
			lc.UserLogin.Bridge.Log.Info().Str("peer", mid).Msg("Peer has Letter Sealing disabled, will send plain text")
		}
		return 0, "", err
	}
	keyID, err := res.KeyID.Int64()
	if err != nil {
		return 0, "", err
	}
	pk := peerKeyInfo{raw: int(keyID), pub: res.PublicKey}
	lc.cacheMu.Lock()
	lc.peerKeys[mid] = pk
	lc.cacheMu.Unlock()
	if lc.E2EE != nil {
		lc.E2EE.RegisterPeerPublicKey(pk.raw, pk.pub)
	}
	return pk.raw, pk.pub, nil
}

// isGroupNoE2EE checks if a group is cached as having no E2EE shared key.
func (lc *LineClient) isGroupNoE2EE(chatMid string) bool {
	lc.cacheMu.Lock()
	defer lc.cacheMu.Unlock()
	if lc.noE2EEGroups == nil {
		return false
	}
	checkedAt, ok := lc.noE2EEGroups[chatMid]
	return ok && time.Since(checkedAt) < noE2EETTL
}

// markGroupNoE2EE caches a group as having no E2EE shared key.
func (lc *LineClient) markGroupNoE2EE(chatMid string) {
	lc.cacheMu.Lock()
	defer lc.cacheMu.Unlock()
	if lc.noE2EEGroups == nil {
		lc.noE2EEGroups = make(map[string]time.Time)
	}
	lc.noE2EEGroups[chatMid] = time.Now()
}

// clearGroupNoE2EE removes a group from the noE2EE cache (e.g., when we receive encrypted messages).
func (lc *LineClient) clearGroupNoE2EE(chatMid string) {
	lc.cacheMu.Lock()
	defer lc.cacheMu.Unlock()
	delete(lc.noE2EEGroups, chatMid)
}

func (lc *LineClient) cacheGroupMemberMIDs(chatMid string, mids []string) {
	// A self-only result is ambiguous: LINE sometimes returns an empty member
	// map for active groups. Keep a previously complete list for fallback.
	if len(mids) <= 1 {
		return
	}
	lc.cacheMu.Lock()
	defer lc.cacheMu.Unlock()
	if lc.groupMemberCache == nil {
		lc.groupMemberCache = make(map[string][]string)
	}
	lc.groupMemberCache[chatMid] = append([]string(nil), mids...)
}

func joinedGroupMemberMIDs(group *line.GroupExtra, ownMID string) ([]string, bool) {
	seen := make(map[string]struct{})
	if group != nil {
		for mid := range group.MemberMids {
			if isUserMID(mid) {
				seen[mid] = struct{}{}
			}
		}
	}
	if isUserMID(ownMID) {
		seen[ownMID] = struct{}{}
	}

	mids := make([]string, 0, len(seen))
	for mid := range seen {
		mids = append(mids, mid)
	}
	return mids, group != nil && len(group.InviteeMids) > 0
}

// getChatMemberMIDs fetches joined member MIDs for a group chat via GetChats.
// Pending invitees are deliberately excluded: LINE validates group keys against the
// current joined-member set and rejects keys that include invitees.
func (lc *LineClient) getChatMemberMIDs(ctx context.Context, chatMid string) ([]string, bool, error) {
	client := lc.newClient()
	chats, err := client.GetChats([]string{chatMid}, true, true)
	if err != nil {
		if lc.shouldAttemptTokenRecovery(ctx, err) {
			if errRecover := lc.recoverToken(ctx); errRecover == nil {
				client = lc.newClient()
				chats, err = client.GetChats([]string{chatMid}, true, true)
			}
		}
		if err != nil {
			return nil, false, fmt.Errorf("getChats failed for %s: %w", chatMid, err)
		}
	}
	if len(chats.Chats) == 0 {
		return nil, false, fmt.Errorf("chat %s not found", chatMid)
	}
	chat := chats.Chats[0]
	if chat.Extra.GroupExtra == nil {
		return nil, false, fmt.Errorf("chat %s has no group extra", chatMid)
	}
	group := chat.Extra.GroupExtra
	mids, hasPendingInvitees := joinedGroupMemberMIDs(group, lc.Mid)
	if len(mids) == 0 {
		return nil, hasPendingInvitees, fmt.Errorf("chat %s has no joined members", chatMid)
	}

	// Cache complete results for fallback use. Do not replace a richer cached
	// list when LINE returns only the caller.
	lc.cacheGroupMemberMIDs(chatMid, mids)

	return mids, hasPendingInvitees, nil
}

// autoRegisterGroupKey fetches group members, then registers a new E2EE group key
// for the chat. This is called when fetchAndUnwrapGroupKey finds no key exists.
func (lc *LineClient) autoRegisterGroupKey(ctx context.Context, chatMid string) error {
	members, hasPendingInvitees, err := lc.getChatMemberMIDs(ctx, chatMid)
	if err != nil {
		return fmt.Errorf("getChatMemberMIDs: %w", err)
	}

	// If getChatMemberMIDs returned only ourself, the server likely returned
	// an empty MemberMids map (known LINE API issue). Fall back to cached
	// member list from CreateGroup or a prior successful fetch.
	if len(members) == 1 && members[0] == lc.Mid && !hasPendingInvitees {
		lc.cacheMu.Lock()
		cached, ok := lc.groupMemberCache[chatMid]
		cached = append([]string(nil), cached...)
		lc.cacheMu.Unlock()
		if ok && len(cached) > 1 {
			lc.UserLogin.Bridge.Log.Warn().Str("chat_mid", chatMid).
				Msg("GetChats returned only self MID, falling back to cached member list")
			members = cached
		}
	}

	// Last resort: query Matrix room members via the bridge API.
	if len(members) == 1 && members[0] == lc.Mid && !hasPendingInvitees {
		matrixMembers, err := lc.getGroupMemberMIDsViaMatrix(ctx, chatMid)
		if err != nil {
			lc.UserLogin.Bridge.Log.Warn().Err(err).Str("chat_mid", chatMid).
				Msg("Matrix member fallback also failed")
		} else if len(matrixMembers) > 1 {
			lc.UserLogin.Bridge.Log.Warn().Str("chat_mid", chatMid).
				Int("members", len(matrixMembers)).
				Msg("GetChats returned only self MID, falling back to Matrix room members")
			members = matrixMembers
		}
	}

	return lc.registerGroupKey(ctx, chatMid, members)
}

// getGroupMemberMIDsViaMatrix queries the Matrix room's member list via the bridge
// API and converts ghost user IDs back to LINE MIDs. This is a fallback when the
// LINE API's GetChats withMembers returns an empty MemberMids map.
func (lc *LineClient) getGroupMemberMIDsViaMatrix(ctx context.Context, chatMid string) (_ []string, err error) {
	portalKey := networkid.PortalKey{
		ID:       makePortalID(chatMid),
		Receiver: lc.UserLogin.ID,
	}
	portal, err := lc.UserLogin.Bridge.GetPortalByKey(ctx, portalKey)
	if err != nil {
		return nil, fmt.Errorf("get portal: %w", err)
	}
	if portal == nil || portal.MXID == "" {
		return nil, fmt.Errorf("portal has no Matrix room")
	}

	matrixMembers, err := lc.UserLogin.Bridge.Matrix.GetMembers(ctx, portal.MXID)
	if err != nil {
		return nil, fmt.Errorf("get matrix members: %w", err)
	}

	mids := make([]string, 0, len(matrixMembers))
	seen := make(map[string]struct{}, len(matrixMembers)+1)
	for mxid := range matrixMembers {
		// Skip the bridge user's own Matrix account if present.
		if mxid == lc.UserLogin.UserMXID {
			continue
		}
		// Parse the ghost MXID back to a network ID (LINE MID).
		if netID, ok := lc.UserLogin.Bridge.Matrix.ParseGhostMXID(mxid); ok {
			mid := string(netID)
			if isUserMID(mid) && !lc.isOwnMID(mid) {
				if _, exists := seen[mid]; exists {
					continue
				}
				seen[mid] = struct{}{}
				mids = append(mids, mid)
			}
		}
	}

	if len(mids) == 0 {
		return nil, fmt.Errorf("no LINE members found via Matrix")
	}

	// Include the bridge user's MID when it has a valid user prefix.
	if isUserMID(lc.Mid) {
		mids = append(mids, lc.Mid)
	}

	lc.UserLogin.Bridge.Log.Debug().Str("chat_mid", chatMid).
		Int("matrix_members", len(matrixMembers)).
		Int("resolved_mids", len(mids)).
		Msg("Resolved group members via Matrix room")

	return mids, nil
}

func (lc *LineClient) ensurePeerKeyByID(ctx context.Context, mid string, keyID int) (int, string, error) {
	lc.cacheMu.Lock()
	if lc.peerKeys == nil {
		lc.peerKeys = make(map[string]peerKeyInfo)
	}
	cached, ok := lc.peerKeys[mid]
	lc.cacheMu.Unlock()
	if ok && cached.raw == keyID && cached.pub != "" {
		if lc.E2EE != nil {
			lc.E2EE.RegisterPeerPublicKey(cached.raw, cached.pub)
		}
		return cached.raw, cached.pub, nil
	}

	// keyVersion 1
	_, res, err := callLineResult(lc, ctx, func(client *line.Client) (*line.E2EEPublicKey, error) {
		return getE2EEPublicKeyWithClient(client, mid, 1, keyID)
	})
	if err != nil {
		return 0, "", err
	}

	resKeyID, err := res.KeyID.Int64()
	if err != nil {
		return 0, "", err
	}

	if int(resKeyID) != keyID {
		return 0, "", fmt.Errorf("fetched key ID %d does not match requested %d", resKeyID, keyID)
	}

	pk := peerKeyInfo{raw: int(resKeyID), pub: res.PublicKey}
	// Cache the fetched key so subsequent lookups reuse it.
	lc.cacheMu.Lock()
	lc.peerKeys[mid] = pk
	lc.cacheMu.Unlock()
	if lc.E2EE != nil {
		lc.E2EE.RegisterPeerPublicKey(pk.raw, pk.pub)
	}
	return pk.raw, pk.pub, nil
}

func (lc *LineClient) ensurePeerKeyForMessage(ctx context.Context, msg *line.Message) {
	if lc.E2EE == nil || len(msg.Chunks) < 5 {
		return
	}

	// If we receive an encrypted message from a peer we cached as noE2EE,
	// they must have enabled Letter Sealing — invalidate the cache.
	lc.cacheMu.Lock()
	if lc.peerKeys != nil {
		if cached, ok := lc.peerKeys[msg.From]; ok && cached.noE2EE {
			delete(lc.peerKeys, msg.From)
			lc.cacheMu.Unlock()
			lc.UserLogin.Bridge.Log.Info().Str("peer", msg.From).Msg("Received encrypted message from peer previously cached as noE2EE, invalidating cache")
		} else {
			lc.cacheMu.Unlock()
		}
	} else {
		lc.cacheMu.Unlock()
	}

	senderKeyID, err1 := e2ee.DecodeKeyID(msg.Chunks[len(msg.Chunks)-2])
	if err1 != nil {
		lc.UserLogin.Bridge.Log.Warn().Err(err1).Msg("Failed to decode sender key ID")
		return
	}
	myRaw, _, errMy := lc.E2EE.MyKeyIDs()
	if errMy != nil {
		lc.UserLogin.Bridge.Log.Warn().Err(errMy).Msg("Failed to get own key IDs")
		return
	}

	// For group messages, chunks are [first, body, tag, senderKeyID, groupKeyID].
	// We only need the sender's public key to create the decryption channel.
	if ToType(msg.ToType) == ToRoom || ToType(msg.ToType) == ToGroup {
		if lc.E2EE.IsMyKey(senderKeyID) {
			return
		}
		if lc.E2EE.HasPeerPublicKey(senderKeyID) {
			return
		}
		lc.UserLogin.Bridge.Log.Debug().Int("peer_key_id", senderKeyID).Str("peer_mid", msg.From).Msg("Fetching peer public key for group decrypt")
		if _, _, err := lc.ensurePeerKeyByID(ctx, msg.From, senderKeyID); err != nil {
			lc.UserLogin.Bridge.Log.Warn().Err(err).Str("peer", msg.From).Int("key_id", senderKeyID).Msg("Failed to fetch sender peer key for group decrypt")
		}
		return
	}

	// 1:1 message handling
	receiverKeyID, err2 := e2ee.DecodeKeyID(msg.Chunks[len(msg.Chunks)-1])
	if err2 != nil {
		lc.UserLogin.Bridge.Log.Warn().Err(err2).Msg("Failed to decode receiver key ID")
		return
	}
	peerRaw := senderKeyID
	peerMid := msg.From
	// Treat the message as self-sent if the sender key is any of OUR devices'
	// keys (the user's keychain shares private keys across devices). myRaw
	// only points to the latest one, so we must check the full set.
	if lc.E2EE.IsMyKey(senderKeyID) {
		peerRaw = receiverKeyID
		peerMid = msg.To
	}
	if peerRaw == 0 || peerRaw == myRaw {
		return
	}
	if lc.E2EE.HasPeerPublicKey(peerRaw) {
		return
	}
	lc.UserLogin.Bridge.Log.Debug().Int("peer_key_id", peerRaw).Str("peer_mid", peerMid).Msg("Fetching peer public key for decrypt")
	if _, _, err := lc.ensurePeerKeyByID(ctx, peerMid, peerRaw); err != nil {
		lc.UserLogin.Bridge.Log.Debug().Err(err).Int("key_id", peerRaw).Msg("ensurePeerKeyByID failed, trying NegotiateE2EEPublicKey")
		if _, _, err2 := lc.ensurePeerKey(ctx, peerMid); err2 != nil {
			lc.UserLogin.Bridge.Log.Warn().Err(err2).Str("peer", peerMid).Int("key_id", peerRaw).Msg("Failed to fetch peer key for decrypt")
		}
	}
}
