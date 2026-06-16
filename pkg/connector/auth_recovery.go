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

func callLineWithRecovery[T any](ctx context.Context, deps lineCallDeps[T]) (*line.Client, T, error) {
	client := deps.newClient()
	res, err := deps.call(client)
	if err == nil || !deps.isAuthError(err) {
		return client, res, err
	}

	if errRecover := deps.recover(ctx); errRecover != nil {
		var zero T
		return client, zero, fmt.Errorf("failed to recover token after LINE auth error: %w", errRecover)
	}

	client = deps.newClient()
	res, err = deps.call(client)
	return client, res, err
}

func (lc *LineClient) isTokenError(err error) bool {
	if line.IsNoUsableE2EEGroupKey(err) || line.IsNoUsableE2EEPublicKey(err) {
		return false
	}
	return line.IsAuthError(err)
}

func (lc *LineClient) callLine(ctx context.Context, call func(*line.Client) error) (*line.Client, error) {
	client, _, err := callLineWithRecovery(ctx, lineCallDeps[struct{}]{
		newClient:   func() *line.Client { return line.NewClient(lc.AccessToken) },
		recover:     lc.recoverToken,
		isAuthError: lc.isTokenError,
		call: func(client *line.Client) (struct{}, error) {
			return struct{}{}, call(client)
		},
	})
	return client, err
}

func callLineResult[T any](lc *LineClient, ctx context.Context, call func(*line.Client) (T, error)) (*line.Client, T, error) {
	return callLineWithRecovery(ctx, lineCallDeps[T]{
		newClient:   func() *line.Client { return line.NewClient(lc.AccessToken) },
		recover:     lc.recoverToken,
		isAuthError: lc.isTokenError,
		call:        call,
	})
}
