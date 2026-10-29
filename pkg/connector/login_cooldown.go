package connector

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/beeper/line/pkg/line"
)

const (
	// loginRateLimitCooldown is how long the bridge stops starting new login
	// attempts for an account after LINE rejected a login as rate limited.
	// Repeated attempts keep the account locked, so the bridge waits instead
	// of retrying.
	loginRateLimitCooldown = 15 * time.Minute
	// loginRetryLaterCooldown is the shorter wait used when LINE only asked the
	// client to come back later.
	loginRetryLaterCooldown = 5 * time.Minute
	// loginRetryLaterMessage is shown when LINE asked the client to come back
	// later instead of naming a rate limit.
	loginRetryLaterMessage = "LINE asked us to try the sign-in again later. Please wait a few minutes before trying again."
)

// loginRateLimitKind classifies how strongly a login rejection looks like an
// account login rate limit.
type loginRateLimitKind int

const (
	loginRateLimitNone loginRateLimitKind = iota
	loginRateLimitLocked
	loginRateLimitRetryLater
	loginRateLimitVerificationUnavailable
)

// loginCooldown tracks when each account may try to sign in again. It is
// in-memory on purpose: the limit is enforced by LINE, so the bridge only
// needs to avoid making it worse.
type loginCooldown struct {
	mu     sync.Mutex
	until  map[string]time.Time
	active map[string]bool
}

func (c *loginCooldown) acquire(userID string) bool {
	if c == nil || userID == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == nil {
		c.active = make(map[string]bool)
	}
	if c.active[userID] {
		return false
	}
	c.active[userID] = true
	return true
}

func (c *loginCooldown) release(userID string) {
	if c == nil || userID == "" {
		return
	}
	c.mu.Lock()
	delete(c.active, userID)
	c.mu.Unlock()
}

// block stops new login attempts for the account until the given time.
func (c *loginCooldown) block(userID string, until time.Time) {
	if c == nil || userID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if c.until == nil {
		c.until = make(map[string]time.Time)
	} else {
		for account, expiry := range c.until {
			if !expiry.After(now) {
				delete(c.until, account)
			}
		}
	}
	if current, ok := c.until[userID]; !ok || until.After(current) {
		c.until[userID] = until
	}
}

// remaining returns how long the account still has to wait.
func (c *loginCooldown) remaining(userID string) time.Duration {
	if c == nil || userID == "" {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	until, ok := c.until[userID]
	if !ok {
		return 0
	}
	remaining := time.Until(until)
	if remaining <= 0 {
		delete(c.until, userID)
		return 0
	}
	return remaining
}

// classifyLoginRateLimit reports whether a login rejection is an account login
// rate limit. LINE does not name the condition in every response, so the
// explicit signals are used: HTTP 429, the fixed "too many login attempts"
// reason, or a request to come back later. If the error carries a structured
// AuthError, its category is authoritative.
func classifyLoginRateLimit(err error) loginRateLimitKind {
	if err == nil {
		return loginRateLimitNone
	}

	// Prefer the structured AuthError classification when available.
	var ae *line.AuthError
	if errors.As(err, &ae) {
		switch ae.Category {
		case line.AuthErrorTooManyAttempts:
			return loginRateLimitLocked
		case line.AuthErrorRetryLater:
			return loginRateLimitRetryLater
		case line.AuthErrorVerificationTemporarilyUnavailable:
			return loginRateLimitVerificationUnavailable
		default:
			return loginRateLimitNone
		}
	}

	if detail, ok := line.QRLoginFailureDetail(err); ok {
		if detail.HTTPStatus == http.StatusTooManyRequests {
			return loginRateLimitLocked
		}
		if isLoginRateLimitText(detail.Reason) || isLoginRateLimitText(detail.Alert) {
			return loginRateLimitLocked
		}
		if detail.ExceptionLabel == "RETRY_LATER" {
			return loginRateLimitRetryLater
		}
		return loginRateLimitNone
	}
	details := parseLoginErrorDetails(err)
	if details.HTTPStatus == http.StatusTooManyRequests {
		return loginRateLimitLocked
	}
	if isLoginRateLimitText(details.ErrorReason) || isLoginRateLimitText(details.ErrorMessage) {
		return loginRateLimitLocked
	}
	if isVerificationTemporarilyUnavailableText(details.ErrorReason) || isVerificationTemporarilyUnavailableText(details.ErrorMessage) {
		return loginRateLimitVerificationUnavailable
	}
	if isRetryLaterText(details.ErrorReason) || isRetryLaterText(details.ErrorMessage) {
		return loginRateLimitRetryLater
	}
	return loginRateLimitNone
}

func isVerificationTemporarilyUnavailableText(text string) bool {
	return line.SanitizeProviderAlert(text) == "verification_temporarily_unavailable"
}

func isLoginRateLimitText(text string) bool {
	category := line.SanitizeProviderAlert(text)
	return category == "too_many_login_attempts" || category == "blocked_user"
}

func isRetryLaterText(text string) bool {
	text = strings.TrimSpace(text)
	return strings.EqualFold(text, "retry later") || strings.EqualFold(text, "RETRY_LATER")
}

// noteLoginRateLimit records a rate limit rejection so the next attempt waits
// instead of extending the lock.
func (ll *LineEmailLogin) noteLoginRateLimit(err error) {
	if ll.cooldown == nil || ll.User == nil {
		return
	}
	now := time.Now()
	var authErr *line.AuthError
	_ = errors.As(err, &authErr)
	var delay time.Duration
	switch classifyLoginRateLimit(err) {
	case loginRateLimitLocked:
		delay = loginRateLimitCooldown
	case loginRateLimitRetryLater:
		delay = loginRetryLaterCooldown
	case loginRateLimitVerificationUnavailable:
		delay = verificationTemporarilyUnavailableCooldown
	}
	if authErr != nil && authErr.RetryAfter != nil && *authErr.RetryAfter > delay {
		delay = *authErr.RetryAfter
	}
	if delay > 0 {
		ll.cooldown.block(string(ll.User.MXID), now.Add(delay))
	}
}

// loginCooldownError asks the user to wait while the account is still inside a
// rate limit window.
func (ll *LineEmailLogin) loginCooldownError() error {
	if ll.cooldown == nil || ll.User == nil {
		return nil
	}
	remaining := ll.cooldown.remaining(string(ll.User.MXID))
	if remaining <= 0 {
		return nil
	}
	return ErrLoginRateLimited.WithMessage(
		"LINE is limiting how often this account can sign in. Please try again in %s.",
		formatLoginWait(remaining),
	)
}

func formatLoginWait(remaining time.Duration) string {
	minutes := int((remaining + time.Minute - 1) / time.Minute)
	if minutes < 1 {
		minutes = 1
	}
	if minutes == 1 {
		return "1 minute"
	}
	return fmt.Sprintf("%d minutes", minutes)
}
