package connector

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/bridgeconfig"
	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type loginCommandIntent struct {
	bridgev2.MatrixAPI
	replies []string
}

func (i *loginCommandIntent) GetMXID() id.UserID { return "@bot:example.com" }
func (*loginCommandIntent) IsDoublePuppet() bool { return false }
func (i *loginCommandIntent) SendMessage(_ context.Context, _ id.RoomID, _ event.Type, content *event.Content, _ *bridgev2.MatrixSendExtra) (*mautrix.RespSendEvent, error) {
	i.replies = append(i.replies, content.AsMessage().Body)
	return &mautrix.RespSendEvent{}, nil
}

type loginCommandMatrix struct {
	bridgev2.MatrixConnector
	bot *loginCommandIntent
}

func (*loginCommandMatrix) Init(*bridgev2.Bridge)           {}
func (m *loginCommandMatrix) BotIntent() bridgev2.MatrixAPI { return m.bot }
func (*loginCommandMatrix) SendMessageStatus(context.Context, *bridgev2.MessageStatus, *bridgev2.MessageStatusEventInfo) {
}

type loginCommandNetwork struct {
	LineConnector
	chosen string
}

func (n *loginCommandNetwork) CreateLogin(_ context.Context, _ *bridgev2.User, flow string) (bridgev2.LoginProcess, error) {
	n.chosen = flow
	return nil, errors.New("test authentication dispatch")
}

// This test goes through mautrix-go's actual command processor and the handler
// installed by LineConnector.Init, not just the handler function in isolation.
func TestRegisteredLineLoginCommandDispatch(t *testing.T) {
	ctx := context.Background()
	db, err := dbutil.NewWithDialect("file:"+filepath.ToSlash(filepath.Join(t.TempDir(), "bridge.db")), "sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	network := &loginCommandNetwork{LineConnector: LineConnector{Config: Config{QRLogin: true}}}
	bot := &loginCommandIntent{}
	matrix := &loginCommandMatrix{bot: bot}
	br := bridgev2.NewBridge("test", db, zerolog.New(io.Discard), &bridgeconfig.BridgeConfig{CommandPrefix: "!line"}, matrix, network, func(br *bridgev2.Bridge) bridgev2.CommandProcessor {
		return commands.NewProcessor(br)
	})
	if network.br != br {
		t.Fatalf("connector Init was not called: connector bridge %p, expected %p", network.br, br)
	}
	if _, ok := br.Commands.(*commands.Processor); !ok {
		t.Fatalf("commands processor has unexpected type %T", br.Commands)
	}
	if !commands.CommandLogin.RequiresLoginPermission {
		t.Fatal("wrapped login command lost framework login permission requirement")
	}
	if !strings.Contains(lineLoginCommand().Help.Args, "qr|email|help") {
		t.Fatal("registered login command help does not list friendly methods")
	}
	if err = br.DB.Upgrade(ctx); err != nil {
		t.Fatal(err)
	}
	user := &bridgev2.User{User: &database.User{MXID: "@owner:example.com", ManagementRoom: "!management:example.com"}, Bridge: br, Permissions: bridgeconfig.Permissions{Login: true, MaxLogins: 5}}
	for _, tc := range []struct {
		name, command, args, wantFlow, wantReply string
		qrEnabled                                bool
	}{
		{"default help", "login", "", "", "login qr", true},
		{"explicit help", "login", "help", "", "login email", true},
		{"qr alias", "login", "qr", LoginFlowIDQR, "Failed to prepare login process", true},
		{"case-insensitive qr alias", "login", "QR", LoginFlowIDQR, "Failed to prepare login process", true},
		{"email alias", "login", "email", LoginFlowIDEmail, "Failed to prepare login process", true},
		{"case-insensitive email alias", "login", "EMAIL", LoginFlowIDEmail, "Failed to prepare login process", true},
		{"qr flow ID", "login", LoginFlowIDQR, LoginFlowIDQR, "Failed to prepare login process", true},
		{"email flow ID", "login", LoginFlowIDEmail, LoginFlowIDEmail, "Failed to prepare login process", true},
		{"unknown method", "login", "invalid", "", "Unknown or unavailable LINE login method", true},
		{"disabled qr", "login", "qr", "", "Unknown or unavailable LINE login method", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			network.chosen = ""
			network.Config.QRLogin = tc.qrEnabled
			bot.replies = nil
			processor := br.Commands.(*commands.Processor)
			processor.Handle(ctx, user.ManagementRoom, "$command", user, tc.command+" "+tc.args, "")
			if network.chosen != tc.wantFlow {
				t.Fatalf("selected flow %q, want %q (replies %q)", network.chosen, tc.wantFlow, bot.replies)
			}
			if len(bot.replies) == 0 || !strings.Contains(bot.replies[len(bot.replies)-1], tc.wantReply) {
				t.Fatalf("replies %q do not contain %q", bot.replies, tc.wantReply)
			}
		})
	}
}
