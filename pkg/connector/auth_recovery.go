package connector

import (
	"context"
	"fmt"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

type lineCallDeps[T any] struct {
	newClient   func() *line.Client
	recover     func(context.Context) error
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

	if errRecover := deps.recover(ctx); errRecover != nil {
		var zero T
		return client, zero, fmt.Errorf("failed to recover token after LINE auth error (%w): %w", err, errRecover)
	}

	client = deps.newClient()
	res, err = deps.call(client)
	return client, res, err
}

func (lc *LineClient) isTokenError(err error) bool {
	if line.IsNoUsableE2EEGroupKey(err) || line.IsNoUsableE2EEPublicKey(err) {
		return false
	}
	if lc.isSessionInvalidated() {
		return false
	}
	if line.IsLoggedOut(err) {
		return false
	}
	return line.IsAuthError(err)
}

func (lc *LineClient) callLine(ctx context.Context, call func(*line.Client) error) (*line.Client, error) {
	return lc.callLineUsing(ctx, nil, call)
}

func (lc *LineClient) callLineUsing(ctx context.Context, client *line.Client, call func(*line.Client) error) (*line.Client, error) {
	client, _, err := callLineWithRecovery(ctx, client, lineCallDeps[struct{}]{
		newClient:   func() *line.Client { return lc.newClient() },
		recover:     func(ctx context.Context) error { return recoverLineToken(lc, ctx) },
		isAuthError: lc.isTokenError,
		call: func(client *line.Client) (struct{}, error) {
			return struct{}{}, call(client)
		},
	})
	if lc.isLoggedOut(err) {
		lc.markLoggedOutByOtherClient(ctx, err)
	}
	return client, err
}

func callLineResult[T any](lc *LineClient, ctx context.Context, call func(*line.Client) (T, error)) (*line.Client, T, error) {
	return callLineResultUsing(lc, ctx, nil, call)
}

func callLineResultUsing[T any](lc *LineClient, ctx context.Context, client *line.Client, call func(*line.Client) (T, error)) (*line.Client, T, error) {
	client, res, err := callLineWithRecovery(ctx, client, lineCallDeps[T]{
		newClient:   func() *line.Client { return lc.newClient() },
		recover:     func(ctx context.Context) error { return recoverLineToken(lc, ctx) },
		isAuthError: lc.isTokenError,
		call:        call,
	})
	if lc.isLoggedOut(err) {
		lc.markLoggedOutByOtherClient(ctx, err)
	}
	return client, res, err
}
