package connector

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

func TestPollLoopRebuildsSSEClientAfterReconnect(t *testing.T) {
	oldGetLastOpRevision := getLastOpRevisionWithClient
	oldListenSSE := listenSSEWithClient
	oldReconnectDelay := sseReconnectDelay
	t.Cleanup(func() {
		getLastOpRevisionWithClient = oldGetLastOpRevision
		listenSSEWithClient = oldListenSSE
		sseReconnectDelay = oldReconnectDelay
	})

	getLastOpRevisionWithClient = func(*line.Client) (int64, error) {
		return 1234, nil
	}
	sseReconnectDelay = time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	lc := &LineClient{
		AccessToken: "old",
		UserLogin: &bridgev2.UserLogin{
			Bridge: &bridgev2.Bridge{Log: zerolog.New(io.Discard)},
		},
	}

	var tokens []string
	listenSSEWithClient = func(client *line.Client, ctx context.Context, localRev int64, handler func(eventType, data string)) error {
		if localRev != 1234 {
			t.Fatalf("localRev = %d, want 1234", localRev)
		}
		tokens = append(tokens, client.AccessToken)
		if len(tokens) == 1 {
			lc.setTokens("new", "")
			return io.EOF
		}
		cancel()
		return context.Canceled
	}

	lc.wg.Add(1)
	lc.pollLoop(ctx)

	if len(tokens) != 2 {
		t.Fatalf("SSE attempts = %d, want 2", len(tokens))
	}
	if tokens[0] != "old" || tokens[1] != "new" {
		t.Fatalf("SSE tokens = %v, want [old new]", tokens)
	}
}
