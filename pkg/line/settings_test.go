package line

import (
	"context"
	"testing"
)

func TestUpdateSettingsAttributes2RequestBodyKeepsExplicitFalse(t *testing.T) {
	notificationDisabledWithSub := false
	var body string
	client := newReactionTestClient(
		t,
		"/api/talk/thrift/Talk/TalkService/updateSettingsAttributes2",
		&body,
	)

	err := client.UpdateSettingsAttributes2Context(
		context.Background(),
		1994881164,
		[]int{SettingsAttributeNotificationDisabledWithSub},
		Settings{NotificationDisabledWithSub: &notificationDisabledWithSub},
	)
	if err != nil {
		t.Fatal(err)
	}

	const want = `[1994881164,[16],{"notificationDisabledWithSub":false}]`
	if body != want {
		t.Fatalf("request body = %s, want %s", body, want)
	}
}

func TestUpdateSettingsAttributes2PreservesAuthErrorDetails(t *testing.T) {
	notificationDisabledWithSub := false
	client := newReactionTestClientWithResponse(
		t,
		"/api/talk/thrift/Talk/TalkService/updateSettingsAttributes2",
		`{"code":10051,"message":"RESPONSE_ERROR","data":{"name":"TalkException","code":119,"reason":"Access token refresh required"}}`,
		nil,
	)

	err := client.UpdateSettingsAttributes2Context(
		context.Background(),
		123,
		[]int{SettingsAttributeNotificationDisabledWithSub},
		Settings{NotificationDisabledWithSub: &notificationDisabledWithSub},
	)
	if err == nil {
		t.Fatal("expected non-zero wrapper error")
	}
	if !IsAuthError(err) {
		t.Fatalf("expected auth error details to be preserved in %q", err)
	}
}

func TestUpdateSettingsAttributes2ContextCancellation(t *testing.T) {
	notificationDisabledWithSub := false
	testRPCContextCancellation(t, "updateSettingsAttributes2", func(client *Client, ctx context.Context) error {
		return client.UpdateSettingsAttributes2Context(
			ctx,
			123,
			[]int{SettingsAttributeNotificationDisabledWithSub},
			Settings{NotificationDisabledWithSub: &notificationDisabledWithSub},
		)
	})
}
