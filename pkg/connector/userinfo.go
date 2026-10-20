package connector

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"go.mau.fi/util/jsontime"
	"go.mau.fi/util/ptr"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"github.com/beeper/line/pkg/connector/handlers"
	"github.com/beeper/line/pkg/line"
)

var _ bridgev2.IdentifierResolvingNetworkAPI = (*LineClient)(nil)

func (lc *LineClient) HandleMatrixReadReceipt(ctx context.Context, read *bridgev2.MatrixReadReceipt) error {
	if read.ReadUpTo.IsZero() && read.EventID == "" {
		return nil
	}

	targetID := string(read.EventID)
	if read.EventID != "" {
		msg, err := lc.UserLogin.Bridge.DB.Message.GetPartByMXID(ctx, read.EventID)
		if err == nil && msg != nil && msg.ID != "" {
			targetID = string(msg.ID)
		}
	}

	if targetID == "" || strings.HasPrefix(targetID, "$") {
		return nil
	}

	_, err := lc.callLine(ctx, func(client *line.Client) error {
		return client.SendChatChecked(string(read.Portal.ID), targetID)
	})
	return err
}

func (lc *LineClient) GetCapabilities(ctx context.Context, portal *bridgev2.Portal) *event.RoomFeatures {
	caps := &event.RoomFeatures{
		ID:               "dev.highest.matrix.line.capabilities.2026_10_14",
		MaxTextLength:    5000,
		Reply:            event.CapLevelFullySupported,
		Reaction:         event.CapLevelPartialSupport,
		ReactionCount:    1,
		AllowedReactions: getLineAllowedReactions(),
		ReadReceipts:     true,
		Delete:           event.CapLevelFullySupported,
		DeleteMaxAge:     &jsontime.Seconds{Duration: 24 * time.Hour},
		DeleteChat:       true,
		LocationMessage:  event.CapLevelPartialSupport,
		// Pending LINE group invitations surface as Beeper message requests. AcceptWithMessage
		// is intentionally left unset so that sending a message implicitly accepts the invite
		// (bridgev2 autoAcceptMessageRequest) — LINE rejects messages to un-joined groups.
		MessageRequest: &event.MessageRequestFeatures{
			AcceptWithButton: event.CapLevelFullySupported,
		},
		File: event.FileFeatureMap{
			event.MsgImage: {
				Caption: event.CapLevelRejected,
				MaxSize: handlers.BeeperMaxFileSize,
				MimeTypes: map[string]event.CapabilitySupportLevel{
					"image/jpeg":    event.CapLevelFullySupported,
					"image/png":     event.CapLevelFullySupported,
					"image/gif":     event.CapLevelFullySupported,
					"image/webp":    event.CapLevelFullySupported,
					"image/avif":    event.CapLevelFullySupported,
					"image/bmp":     event.CapLevelFullySupported,
					"image/tiff":    event.CapLevelFullySupported,
					"image/svg+xml": event.CapLevelFullySupported,
					"image/heic":    event.CapLevelFullySupported,
					"image/heif":    event.CapLevelFullySupported,
					"image/jxl":     event.CapLevelFullySupported,
				},
			},
			event.MsgFile: {
				Caption: event.CapLevelRejected,
				MaxSize: handlers.BeeperMaxFileSize,
				MimeTypes: map[string]event.CapabilitySupportLevel{
					"image/gif": event.CapLevelFullySupported,
					"*/*":       event.CapLevelFullySupported,
				},
			},
			event.MsgVideo: {
				Caption: event.CapLevelRejected,
				MaxSize: handlers.BeeperMaxFileSize,
				MimeTypes: map[string]event.CapabilitySupportLevel{
					"video/mp4":        event.CapLevelFullySupported,
					"video/webm":       event.CapLevelFullySupported,
					"video/quicktime":  event.CapLevelFullySupported,
					"video/3gpp":       event.CapLevelFullySupported,
					"video/x-matroska": event.CapLevelFullySupported,
					"video/mpeg":       event.CapLevelFullySupported,
					"video/ogg":        event.CapLevelFullySupported,
					"video/x-msvideo":  event.CapLevelFullySupported,
				},
			},
			event.MsgAudio: {
				Caption: event.CapLevelRejected,
				MaxSize: handlers.BeeperMaxFileSize,
				MimeTypes: map[string]event.CapabilitySupportLevel{
					"audio/mpeg":  event.CapLevelFullySupported,
					"audio/ogg":   event.CapLevelFullySupported,
					"audio/mp4":   event.CapLevelFullySupported,
					"audio/x-m4a": event.CapLevelFullySupported,
					"audio/aac":   event.CapLevelFullySupported,
					"audio/wav":   event.CapLevelFullySupported,
					"audio/x-wav": event.CapLevelFullySupported,
					"audio/flac":  event.CapLevelFullySupported,
					"audio/opus":  event.CapLevelFullySupported,
					"audio/webm":  event.CapLevelFullySupported,
					"audio/amr":   event.CapLevelFullySupported,
					"audio/3gpp":  event.CapLevelFullySupported,
				},
			},
			event.CapMsgVoice: {
				Caption: event.CapLevelRejected,
				MaxSize: handlers.BeeperMaxFileSize,
				MimeTypes: map[string]event.CapabilitySupportLevel{
					"audio/ogg":   event.CapLevelFullySupported,
					"audio/mp4":   event.CapLevelFullySupported,
					"audio/mpeg":  event.CapLevelFullySupported,
					"audio/x-m4a": event.CapLevelFullySupported,
					"audio/aac":   event.CapLevelFullySupported,
					"audio/wav":   event.CapLevelFullySupported,
					"audio/x-wav": event.CapLevelFullySupported,
					"audio/flac":  event.CapLevelFullySupported,
					"audio/opus":  event.CapLevelFullySupported,
					"audio/webm":  event.CapLevelFullySupported,
					"audio/amr":   event.CapLevelFullySupported,
					"audio/3gpp":  event.CapLevelFullySupported,
				},
			},
		},
	}
	if portal != nil && portal.RoomType == database.RoomTypeDM && !lc.isOwnMID(string(portal.ID)) {
		caps.ID += "+dm"
		caps.BlockUser = true
	}
	return caps
}

func (lc *LineClient) IsThisUser(ctx context.Context, userID networkid.UserID) bool {
	return userID == networkid.UserID(lc.UserLogin.ID)
}

func (lc *LineClient) GetChatInfo(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
	mid := string(portal.ID)
	lowerMid := strings.ToLower(mid)
	if strings.HasPrefix(lowerMid, "c") || strings.HasPrefix(lowerMid, "r") {
		var chat *line.Chat
		if strings.HasPrefix(lowerMid, "r") {
			// Legacy multi-person rooms aren't returned by getChats.
			_, rooms, err := callLineResult(lc, ctx, func(client *line.Client) ([]line.Room, error) {
				return client.GetRoomsV2([]string{mid})
			})
			if err != nil {
				return nil, err
			}
			if len(rooms) == 0 {
				return nil, fmt.Errorf("room not found")
			}
			chat = &line.Chat{
				ChatMid: mid,
				Type:    1,
				Extra:   line.ChatExtra{GroupExtra: &line.GroupExtra{MemberMids: rooms[0].MemberMids}},
			}
		} else {
			_, res, err := callLineResult(lc, ctx, func(client *line.Client) (*line.GetChatsResponse, error) {
				return client.GetChats([]string{mid}, true, true)
			})
			if err != nil {
				return nil, err
			}
			if len(res.Chats) == 0 {
				return nil, fmt.Errorf("chat not found")
			}
			chat = &res.Chats[0]
		}
		info := lc.chatToChatInfo(ctx, chat, true)
		if portal.MXID == "" && lc.shouldSkipDeletedChat(mid, "") {
			lc.stripRemoteMembersFromInitialChatInfo(info)
		}
		return info, nil
	}

	contact := lc.getContact(ctx, string(portal.ID))
	return lc.directChatInfo(mid, contact), nil
}

func (lc *LineClient) directChatInfo(mid string, contact line.Contact) *bridgev2.ChatInfo {
	dmType := database.RoomTypeDM
	chatName := contact.EffectiveDisplayName()
	return &bridgev2.ChatInfo{
		Type:        &dmType,
		Name:        &chatName,
		Avatar:      lc.avatarFromPicturePath(contact.PicturePath),
		UserBlocked: ptr.Ptr(lc.isUserBlocked(mid)),
		Members: &bridgev2.ChatMemberList{
			IsFull: true,
			MemberMap: chatMemberMapFromList([]bridgev2.ChatMember{
				{
					EventSender: bridgev2.EventSender{
						IsFromMe: true,
						Sender:   networkid.UserID(lc.UserLogin.ID),
					},
					Membership: event.MembershipJoin,
					PowerLevel: ptr.Ptr(100),
				},
				{
					EventSender: bridgev2.EventSender{
						Sender: networkid.UserID(mid),
					},
					Membership: event.MembershipJoin,
					PowerLevel: ptr.Ptr(0),
				},
			}),
		},
	}
}

func (lc *LineClient) GetUserInfo(ctx context.Context, ghost *bridgev2.Ghost) (*bridgev2.UserInfo, error) {
	contact := lc.getContact(ctx, string(ghost.ID))
	name := contact.EffectiveDisplayName()
	return &bridgev2.UserInfo{
		Identifiers: []string{string(ghost.ID)},
		Name:        &name,
		Avatar:      lc.avatarFromPicturePath(contact.PicturePath),
	}, nil
}

func (lc *LineClient) getContact(ctx context.Context, mid string) line.Contact {
	lc.cacheMu.Lock()
	cached, ok := lc.contactCache[mid]
	lc.cacheMu.Unlock()
	if ok && time.Since(cached.cachedAt) < contactCacheTTL {
		return cached.Contact
	}

	// Use GetProfile for our own user data
	if mid == lc.Mid || mid == string(lc.UserLogin.ID) {
		_, profile, err := callLineResult(lc, ctx, func(client *line.Client) (*line.Profile, error) {
			return client.GetProfile()
		})
		if err == nil && profile != nil {
			contact := line.Contact{Mid: mid, DisplayName: profile.DisplayName, PicturePath: profile.PicturePath}
			lc.setCachedContact(mid, contact)
			return contact
		}
		return line.Contact{Mid: mid, DisplayName: mid}
	}

	client := lc.newClient()
	client, res, err := callLineResultUsing(lc, ctx, client, func(client *line.Client) (*line.ContactsResponse, error) {
		return client.GetContactsV2([]string{mid})
	})
	if err == nil && res != nil && res.Contacts != nil {
		if wrapper, ok := res.Contacts[mid]; ok {
			lc.setCachedContact(mid, wrapper.Contact)
			return wrapper.Contact
		}
	}

	// Fall back to BuddyService for official/business accounts
	lc.UserLogin.Bridge.Log.Debug().Str("mid", mid).Msg("Contact not found via GetContactsV2, trying BuddyService")
	_, buddy, err := callLineResultUsing(lc, ctx, client, func(client *line.Client) (*line.BuddyProfile, error) {
		return client.GetBuddyProfile(mid)
	})
	if err == nil && buddy != nil {
		lc.UserLogin.Bridge.Log.Debug().Str("mid", mid).Str("display_name", buddy.DisplayName).Str("picture_path", buddy.PicturePath).Msg("Got buddy profile")
		contact := line.Contact{Mid: mid, DisplayName: buddy.DisplayName, PicturePath: buddy.PicturePath}
		lc.setCachedContact(mid, contact)
		return contact
	}
	if err != nil {
		lc.UserLogin.Bridge.Log.Debug().Err(err).Str("mid", mid).Msg("BuddyService lookup also failed")
	}

	return line.Contact{Mid: mid, DisplayName: mid}
}

// setCachedContact stores a contact in the cache under lc.cacheMu.
func (lc *LineClient) setCachedContact(mid string, contact line.Contact) {
	lc.cacheMu.Lock()
	if lc.contactCache == nil {
		lc.contactCache = make(map[string]cachedContact)
	}
	lc.contactCache[mid] = cachedContact{Contact: contact, cachedAt: time.Now()}
	lc.cacheMu.Unlock()
}

func (lc *LineClient) ResolveIdentifier(ctx context.Context, identifier string, createChat bool) (*bridgev2.ResolveIdentifierResponse, error) {
	contact, err := lc.resolveContactIdentifier(ctx, identifier)
	if err != nil || contact == nil {
		return nil, err
	}
	userID := makeUserID(contact.Mid)
	ghost, err := lc.UserLogin.Bridge.GetGhostByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get ghost: %w", err)
	}
	name := contact.EffectiveDisplayName()
	resp := &bridgev2.ResolveIdentifierResponse{
		Ghost:    ghost,
		UserID:   userID,
		UserInfo: &bridgev2.UserInfo{Identifiers: []string{contact.Mid}, Name: &name, Avatar: lc.avatarFromPicturePath(contact.PicturePath)},
	}
	if createChat {
		// Resolve and validate before bridgev2 persists anything. Its canonical
		// portal cache and room-creation event loop handle reuse and concurrency.
		resp.Chat = &bridgev2.CreateChatResponse{
			PortalKey:  networkid.PortalKey{ID: makePortalID(contact.Mid), Receiver: lc.UserLogin.ID},
			PortalInfo: lc.directChatInfo(contact.Mid, *contact),
		}
	}
	return resp, nil
}

func (lc *LineClient) midToResolveIdentifier(ctx context.Context, mid string) *bridgev2.ResolveIdentifierResponse {
	if mid == lc.Mid || mid == string(lc.UserLogin.ID) {
		return nil
	}
	userID := makeUserID(mid)
	ghost, err := lc.UserLogin.Bridge.GetGhostByID(ctx, userID)
	if err != nil {
		return nil
	}
	userInfo, _ := lc.GetUserInfo(ctx, ghost)
	return &bridgev2.ResolveIdentifierResponse{
		Ghost:    ghost,
		UserID:   userID,
		UserInfo: userInfo,
	}
}

func (lc *LineClient) SearchUsers(ctx context.Context, query string) ([]*bridgev2.ResolveIdentifierResponse, error) {
	var results []*bridgev2.ResolveIdentifierResponse

	// Exact public ID and MID lookup is shared with start-chat. Only search
	// falls back to non-unique display-name matches.
	lowerQuery := strings.ToLower(strings.TrimSpace(query))
	if lowerQuery != "" {
		contact, err := lc.resolveContactIdentifier(ctx, query)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err == nil && contact != nil {
			if r := lc.midToResolveIdentifier(ctx, contact.Mid); r != nil {
				return []*bridgev2.ResolveIdentifierResponse{r}, nil
			}
		}
	}

	// Search contacts by display name
	client, allMids, err := callLineResult(lc, ctx, func(client *line.Client) ([]string, error) {
		return client.GetAllContactIdsContext(ctx)
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		lc.UserLogin.Bridge.Log.Warn().Err(err).Msg("Failed to get all contact IDs for search")
		return results, nil
	}

	// Fetch contacts in batches to check display names
	for i := 0; i < len(allMids); i += 50 {
		end := i + 50
		if end > len(allMids) {
			end = len(allMids)
		}
		batch := allMids[i:end]
		contactsResp, err := client.GetContactsV2Context(ctx, batch)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		for _, wrapper := range contactsResp.Contacts {
			c := wrapper.Contact
			if c.Mid == "" {
				continue
			}
			name := strings.ToLower(c.EffectiveDisplayName())
			if lowerQuery == "" || strings.Contains(name, lowerQuery) {
				lc.setCachedContact(c.Mid, c)
				if r := lc.midToResolveIdentifier(ctx, c.Mid); r != nil {
					results = append(results, r)
				}
			}
		}
	}

	return results, nil
}

var _ bridgev2.UserSearchingNetworkAPI = (*LineClient)(nil)

func (lc *LineClient) GetContactList(ctx context.Context) ([]*bridgev2.ResolveIdentifierResponse, error) {
	client, allMids, err := callLineResult(lc, ctx, func(client *line.Client) ([]string, error) {
		return client.GetAllContactIds()
	})
	if err != nil {
		return nil, err
	}

	var results []*bridgev2.ResolveIdentifierResponse
	for i := 0; i < len(allMids); i += 50 {
		end := i + 50
		if end > len(allMids) {
			end = len(allMids)
		}
		batch := allMids[i:end]
		contactsResp, err := client.GetContactsV2(batch)
		if err != nil {
			continue
		}
		for mid, wrapper := range contactsResp.Contacts {
			if wrapper.Contact.Mid == "" {
				continue
			}
			if r := lc.midToResolveIdentifier(ctx, mid); r != nil {
				results = append(results, r)
			}
		}
	}

	return results, nil
}

var _ bridgev2.ContactListingNetworkAPI = (*LineClient)(nil)

func (lc *LineClient) GetAvatar(ctx context.Context, id networkid.AvatarID) ([]byte, error) {
	url := fmt.Sprintf("https://profile.line-scdn.net%s", id)
	resp, err := lc.HTTPClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}
