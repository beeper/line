package connector

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/beeper/line/pkg/line"
)

func isCanonicalUserMID(identifier string) bool {
	if len(identifier) != 33 || identifier[0] != 'u' {
		return false
	}
	for _, ch := range identifier[1:] {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return false
		}
	}
	return true
}

// resolveContactIdentifier never matches display names or creates portals.
// Public IDs are resolved afresh: caching an ID->MID mapping could route a
// reassigned public ID to the wrong recipient. Contact data is cached by MID.
func (lc *LineClient) resolveContactIdentifier(ctx context.Context, identifier string) (*line.Contact, error) {
	if err := lc.checkLineCall(ctx); err != nil {
		return nil, err
	}
	identifier = strings.ToLower(strings.TrimSpace(identifier))
	if identifier == "" || strings.ContainsFunc(identifier, unicode.IsSpace) || strings.ContainsFunc(identifier, unicode.IsControl) {
		return nil, fmt.Errorf("provide a LINE public ID or user MID, not a display name")
	}
	var contact *line.Contact
	var err error
	if isCanonicalUserMID(identifier) {
		lc.cacheMu.Lock()
		cached, ok := lc.contactCache[identifier]
		lc.cacheMu.Unlock()
		if ok && cached.Contact.Mid == identifier && time.Since(cached.cachedAt) < contactCacheTTL {
			return &cached.Contact, nil
		}
		if lc.isOwnMID(identifier) {
			_, profile, lookupErr := callLineResult(lc, ctx, func(client *line.Client) (*line.Profile, error) {
				return client.GetProfileContext(ctx)
			})
			err = lookupErr
			if profile != nil {
				contact = &line.Contact{Mid: profile.Mid, DisplayName: profile.DisplayName, PicturePath: profile.PicturePath}
			}
		} else {
			_, contacts, lookupErr := callLineResult(lc, ctx, func(client *line.Client) (*line.ContactsResponse, error) {
				return client.GetContactsV2Context(ctx, []string{identifier})
			})
			err = lookupErr
			if contacts != nil {
				if wrapper, found := contacts.Contacts[identifier]; found {
					contact = &wrapper.Contact
				}
			}
			if err == nil && contact == nil {
				_, buddy, buddyErr := callLineResult(lc, ctx, func(client *line.Client) (*line.BuddyProfile, error) {
					return client.GetBuddyProfileContext(ctx, identifier)
				})
				err = buddyErr
				if buddy != nil && buddy.Mid != "" {
					contact = &line.Contact{Mid: buddy.Mid, DisplayName: buddy.DisplayName, PicturePath: buddy.PicturePath}
				}
			}
		}
	} else {
		_, contact, err = callLineResult(lc, ctx, func(client *line.Client) (*line.Contact, error) {
			return client.FindContactByUseridContext(ctx, identifier)
		})
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, fmt.Errorf("LINE identifier lookup failed (the account may restrict ID lookup): %w", err)
	}
	if contact == nil || contact.Mid == "" {
		return nil, nil
	}
	if !isCanonicalUserMID(contact.Mid) || isCanonicalUserMID(identifier) && contact.Mid != identifier {
		return nil, fmt.Errorf("LINE lookup returned an invalid or mismatched user MID")
	}
	lc.setCachedContact(contact.Mid, *contact)
	return contact, nil
}
