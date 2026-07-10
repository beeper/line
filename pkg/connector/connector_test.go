package connector

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/bridgeconfig"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/status"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type bridgeStateTestMatrix struct {
	bridgev2.MatrixConnector
	states chan status.BridgeState
}

func (matrix *bridgeStateTestMatrix) GetCapabilities() *bridgev2.MatrixCapabilities {
	return &bridgev2.MatrixCapabilities{AutoJoinInvites: true}
}

func (matrix *bridgeStateTestMatrix) NewUserIntent(_ context.Context, _ id.UserID, accessToken string) (bridgev2.MatrixAPI, string, error) {
	return nil, accessToken, nil
}

func (matrix *bridgeStateTestMatrix) SendBridgeStatus(ctx context.Context, state *status.BridgeState) error {
	select {
	case matrix.states <- *state:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type bridgeStateTestBot struct {
	bridgev2.MatrixAPI
	createRoomCalls  atomic.Int32
	sendMessageCalls atomic.Int32
}

func (bot *bridgeStateTestBot) GetMXID() id.UserID {
	return "@linebot:example.com"
}

func (bot *bridgeStateTestBot) CreateRoom(context.Context, *mautrix.ReqCreateRoom) (id.RoomID, error) {
	bot.createRoomCalls.Add(1)
	return "", errors.New("unexpected management room creation")
}

func (bot *bridgeStateTestBot) SendMessage(
	context.Context,
	id.RoomID,
	event.Type,
	*event.Content,
	*bridgev2.MatrixSendExtra,
) (*mautrix.RespSendEvent, error) {
	bot.sendMessageCalls.Add(1)
	return &mautrix.RespSendEvent{}, nil
}

func TestInitDisablesManagementRoomBridgeStatusNotices(t *testing.T) {
	for _, configuredValue := range []string{"errors", "all", "none"} {
		t.Run(configuredValue, func(t *testing.T) {
			bridge := &bridgev2.Bridge{
				Config: &bridgeconfig.BridgeConfig{BridgeStatusNotices: configuredValue},
			}
			connector := &LineConnector{}

			connector.Init(bridge)

			if connector.br != bridge {
				t.Fatal("Init did not retain the bridge")
			}
			if bridge.Config.BridgeStatusNotices != "none" {
				t.Fatalf("BridgeStatusNotices = %q, want none", bridge.Config.BridgeStatusNotices)
			}
		})
	}
}

func TestBridgeStateUpdatesDoNotUseManagementRoom(t *testing.T) {
	tests := map[string]id.RoomID{
		"room must not be created":   "",
		"existing room stays silent": "!management:example.com",
	}

	for name, managementRoom := range tests {
		t.Run(name, func(t *testing.T) {
			log := zerolog.New(io.Discard)
			matrix := &bridgeStateTestMatrix{states: make(chan status.BridgeState, 2)}
			bot := &bridgeStateTestBot{}
			connector := &LineConnector{}
			bridge := &bridgev2.Bridge{
				Config:        &bridgeconfig.BridgeConfig{BridgeStatusNotices: "errors"},
				Matrix:        matrix,
				Bot:           bot,
				Network:       connector,
				Log:           log,
				BackgroundCtx: context.Background(),
			}
			connector.Init(bridge)

			userMXID := id.UserID("@user:example.com")
			user := &bridgev2.User{
				User: &database.User{
					MXID:           userMXID,
					ManagementRoom: managementRoom,
				},
				Bridge: bridge,
				Log:    log,
			}
			login := &bridgev2.UserLogin{
				UserLogin: &database.UserLogin{
					UserMXID:   userMXID,
					ID:         "line-user",
					RemoteName: "LINE User",
				},
				Bridge: bridge,
				User:   user,
				Log:    log,
			}
			login.BridgeState = bridge.NewBridgeStateQueue(login)
			t.Cleanup(login.BridgeState.Destroy)

			wantStates := []status.BridgeState{
				{
					StateEvent: status.StateBadCredentials,
					Error:      "line-logged-out",
					Message:    "LINE logged this Chrome Extension session out because another LINE client connected.",
					UserAction: status.UserActionRelogin,
				},
				{StateEvent: status.StateConnected},
			}
			for _, state := range wantStates {
				login.BridgeState.Send(state)
			}

			for _, want := range wantStates {
				select {
				case got := <-matrix.states:
					if got.StateEvent != want.StateEvent || got.Error != want.Error || got.UserAction != want.UserAction {
						t.Fatalf("bridge state = %#v, want event=%s error=%s action=%s", got, want.StateEvent, want.Error, want.UserAction)
					}
				case <-time.After(2 * time.Second):
					t.Fatalf("timed out waiting for %s bridge state", want.StateEvent)
				}
			}

			if calls := bot.createRoomCalls.Load(); calls != 0 {
				t.Fatalf("bot CreateRoom calls = %d, want 0", calls)
			}
			if calls := bot.sendMessageCalls.Load(); calls != 0 {
				t.Fatalf("bot SendMessage calls = %d, want 0", calls)
			}
		})
	}
}
