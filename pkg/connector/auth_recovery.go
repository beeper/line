package connector

import (
	"context"
	"fmt"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

type lineCallDeps[T any] struct {
	newClient   func() *line.Client
	recover     func(context.Context, *line.Client, error) (*line.Client, error)
	isAuthError func(error) bool
	call        func(*line.Client) (T, error)
}

var recoverLineToken = func(lc *LineClient, ctx context.Context) error {
	return lc.recoverToken(ctx)
}

func callLineWithRecovery[T any](ctx context.Context, client *line.Client, deps lineCallDeps[T]) (*line.Client, T, error) {
	if client == nil {
		client = deps.newClient()
	}
	res, err := deps.call(client)
	if err == nil || !deps.isAuthError(err) {
		return client, res, err
	}

	recoveredClient, errRecover := deps.recover(ctx, client, err)
	if errRecover != nil {
		var zero T
		return client, zero, fmt.Errorf("failed to recover token after LINE auth error (%w): %w", err, errRecover)
	}
	if recoveredClient == nil {
		return client, res, err
	}

	client = recoveredClient
	res, err = deps.call(client)
	if line.IsLoggedOut(err) {
		// The retry is the final attempt, but a current-token logout still needs
		// to transition the login to BAD_CREDENTIALS. The source-aware recovery
		// callback will ignore it if another token rotation made this retry stale.
		_, _ = deps.recover(ctx, client, err)
	}
	return client, res, err
}

func (lc *LineClient) isTokenError(err error) bool {
	if line.IsNoUsableE2EEGroupKey(err) || line.IsNoUsableE2EEPublicKey(err) {
		return false
	}
	if lc.isSessionInvalidated() {
		return false
	}
	return line.IsAuthError(err)
}

// recoverClientAfterAuthError classifies an auth error using the exact client
// that produced it. Logged-out responses from an older access token are safe to
// retry after a concurrent refresh/re-login; the same response from the current
// token is a genuine forced logout and must invalidate the session.
func (lc *LineClient) recoverClientAfterAuthError(ctx context.Context, failedClient *line.Client, err error) (*line.Client, error) {
	if !lc.isTokenError(err) {
		return nil, nil
	}

	// Wait behind any in-flight refresh/re-login before comparing tokens. This
	// makes the comparison authoritative even when the failed request completed
	// while another goroutine was rotating the access token.
	lc.recoverMu.Lock()
	if ctx.Err() != nil {
		lc.recoverMu.Unlock()
		return nil, ctx.Err()
	}

	currentToken := lc.getAccessToken()
	if failedClient != nil && failedClient.AccessToken != "" && currentToken != "" && failedClient.AccessToken != currentToken && !lc.isSessionInvalidated() {
		if lc.UserLogin != nil && lc.UserLogin.Bridge != nil {
			lc.UserLogin.Bridge.Log.Debug().
				Bool("logged_out", line.IsLoggedOut(err)).
				Bool("stale_access_token", true).
				Msg("Retrying LINE request after response from stale access token")
		}
		lc.recoverMu.Unlock()
		return newLineAPIClient(currentToken), nil
	}

	if line.IsLoggedOut(err) {
		lc.markLoggedOutByOtherClientLocked(ctx, err)
		lc.recoverMu.Unlock()
		return nil, nil
	}
	if lc.recoveryStopped || lc.superseded.Load() {
		lc.recoverMu.Unlock()
		return nil, errLineClientSuperseded
	}
	if lc.isSessionInvalidated() {
		lc.recoverMu.Unlock()
		return nil, errLineSessionInvalidated
	}
	lc.recoverMu.Unlock()

	recoveryToken := lc.getAccessToken()
	if errRecover := recoverLineToken(lc, ctx); errRecover != nil {
		if line.IsLoggedOut(errRecover) {
			// Refresh/re-login errors come from the token that was current when
			// recovery started. Classify them with the same source-aware path in
			// case another serialized recovery rotated that token first.
			return lc.recoverClientAfterAuthError(ctx, newLineAPIClient(recoveryToken), errRecover)
		}
		return nil, errRecover
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if lc.superseded.Load() {
		return nil, errLineClientSuperseded
	}
	if lc.isSessionInvalidated() {
		return nil, errLineSessionInvalidated
	}
	return lc.newClient(), nil
}

func (lc *LineClient) callLine(ctx context.Context, call func(*line.Client) error) (*line.Client, error) {
	return lc.callLineUsing(ctx, nil, call)
}

func (lc *LineClient) callLineUsing(ctx context.Context, client *line.Client, call func(*line.Client) error) (*line.Client, error) {
	client, _, err := callLineWithRecovery(ctx, client, lineCallDeps[struct{}]{
		newClient:   func() *line.Client { return lc.newClient() },
		recover:     lc.recoverClientAfterAuthError,
		isAuthError: lc.isTokenError,
		call: func(client *line.Client) (struct{}, error) {
			return struct{}{}, call(client)
		},
	})
	return client, err
}

func callLineResult[T any](lc *LineClient, ctx context.Context, call func(*line.Client) (T, error)) (*line.Client, T, error) {
	return callLineResultUsing(lc, ctx, nil, call)
}

func callLineResultUsing[T any](lc *LineClient, ctx context.Context, client *line.Client, call func(*line.Client) (T, error)) (*line.Client, T, error) {
	client, res, err := callLineWithRecovery(ctx, client, lineCallDeps[T]{
		newClient:   func() *line.Client { return lc.newClient() },
		recover:     lc.recoverClientAfterAuthError,
		isAuthError: lc.isTokenError,
		call:        call,
	})
	return client, res, err
}
