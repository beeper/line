package connector

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"maunium.net/go/mautrix/bridgev2"

	"github.com/beeper/line/pkg/line"
)

type LineQRLogin struct {
	login   *LineEmailLogin
	client  *line.Client
	session string
	qr      *line.QRCodeResponse
	poll    chan error
	pin     bool

	emailMu sync.Mutex
	email   *LineEmailLogin
}

var _ bridgev2.LoginProcessDisplayAndWait = (*LineQRLogin)(nil)
var _ bridgev2.LoginProcessUserInput = (*LineQRLogin)(nil)
var _ bridgev2.LoginProcessWithOverride = (*LineQRLogin)(nil)

const qrSessionRevokedInstructions = "LINE signed this QR login out right away. Log in with your LINE email and password instead."

func (lq *LineQRLogin) StartWithOverride(ctx context.Context, override *bridgev2.UserLogin) (*bridgev2.LoginStep, error) {
	meta, ok := override.Metadata.(*UserLoginMetadata)
	if !ok {
		return nil, fmt.Errorf("existing LINE login metadata has unexpected type %T", override.Metadata)
	}
	lq.login.ExistingLogin, lq.login.ExistingMetadata = override, meta
	lq.login.Certificate = meta.Certificate
	if meta.ForceFullE2EELogin || len(meta.ExportedKeyMap) == 0 {
		lq.login.Certificate = ""
	}
	return lq.Start(ctx)
}

func (lq *LineQRLogin) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
	ll := lq.login
	ll.mu.Lock()
	if ll.canceled || ll.attemptCtx != nil {
		ll.mu.Unlock()
		return nil, fmt.Errorf("QR login is already started or canceled")
	}
	ll.attemptCtx, ll.attemptCancel = context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
	processCtx := ll.attemptCtx
	ll.mu.Unlock()
	stop := context.AfterFunc(ctx, ll.attemptCancel)
	defer stop()
	client := newLineAPIClient("")
	session, qr, err := client.StartQRLogin(processCtx)
	err = wrapLineLoginError(err)
	ll.mu.Lock()
	ll.attempt = client.LoginAttempt
	canceled := ll.canceled || ctx.Err() != nil || processCtx.Err() != nil
	ll.mu.Unlock()
	if err != nil || canceled {
		lq.Cancel()
		if canceled {
			return nil, context.Canceled
		}
		return nil, err
	}
	lq.client, lq.session, lq.qr = client, session, qr
	timeout := time.Duration(qr.LongPollingIntervalSeconds) * time.Second
	if timeout <= 0 {
		timeout = 150 * time.Second
	}
	lq.startPoll(func(ctx context.Context, session string) error {
		return client.CheckQRCodeVerifiedContext(ctx, session, timeout)
	}, true)
	return &bridgev2.LoginStep{
		Type: bridgev2.LoginStepTypeDisplayAndWait, StepID: "dev.highest.matrix.line.qr",
		Instructions:         "Scan this QR code with the LINE mobile app.",
		DisplayAndWaitParams: &bridgev2.LoginDisplayAndWaitParams{Type: bridgev2.LoginDisplayTypeQR, Data: qr.CallbackURL},
	}, nil
}

func (lq *LineQRLogin) startPoll(check func(context.Context, string) error, retryExpired bool) {
	result := make(chan error, 1)
	lq.poll = result
	ctx, session, qr := lq.login.attemptCtx, lq.session, lq.qr
	go func() {
		count := max(1, min(qr.LongPollingMaxCount, 10))
		for attempt := 0; ; attempt++ {
			err := check(ctx, session)
			if err == nil || ctx.Err() != nil || attempt+1 >= count || !retryExpired || !line.IsQRLoginPollExpired(err) {
				result <- err
				return
			}
			timer := time.NewTimer(min(time.Second<<attempt, 30*time.Second))
			select {
			case <-ctx.Done():
				timer.Stop()
				result <- ctx.Err()
				return
			case <-timer.C:
			}
		}
	}()
}

func (lq *LineQRLogin) Wait(ctx context.Context) (*bridgev2.LoginStep, error) {
	if email := lq.emailLogin(); email != nil {
		return email.Wait(ctx)
	}
	if lq.poll == nil {
		return nil, fmt.Errorf("QR login has not started")
	}
	select {
	case err := <-lq.poll:
		if err != nil {
			lq.Cancel()
			if line.IsQRLoginPollExpired(err) {
				return nil, fmt.Errorf("%w: %w", ErrLoginVerificationFailed, err)
			}
			return nil, err
		}
	case <-lq.login.attemptCtx.Done():
		lq.Cancel()
		return nil, lq.login.attemptCtx.Err()
	case <-ctx.Done():
		lq.Cancel()
		return nil, ctx.Err()
	}
	if !lq.pin {
		verified, err := lq.client.VerifyQRCertificate(ctx, lq.session, lq.login.Certificate)
		if err != nil {
			lq.Cancel()
			return nil, wrapLineLoginError(err)
		}
		if verified {
			return lq.finish(ctx)
		}
		pin, err := lq.client.CreatePinCode(ctx, lq.session)
		if err != nil {
			lq.Cancel()
			return nil, wrapLineLoginError(err)
		}
		lq.pin = true
		lq.startPoll(lq.client.CheckPinCodeVerifiedContext, false)
		return &bridgev2.LoginStep{
			Type: bridgev2.LoginStepTypeDisplayAndWait, StepID: "dev.highest.matrix.line.qr_pin",
			Instructions:         "Enter the displayed PIN in the LINE mobile app.",
			DisplayAndWaitParams: &bridgev2.LoginDisplayAndWaitParams{Type: bridgev2.LoginDisplayTypeCode, Data: pin},
		}, nil
	}
	return lq.finish(ctx)
}

func (lq *LineQRLogin) finish(ctx context.Context) (*bridgev2.LoginStep, error) {
	step, err := lq.completeQR(ctx)
	if err == nil || !strings.Contains(err.Error(), "V3_TOKEN_CLIENT_LOGGED_OUT") {
		return step, err
	}
	lq.login.User.Bridge.Log.Warn().Err(err).Msg("LINE logged out the new QR session, asking for email login")
	email := &LineEmailLogin{
		User:             lq.login.User,
		ExistingLogin:    lq.login.ExistingLogin,
		ExistingMetadata: lq.login.ExistingMetadata,
		finalizeMu:       lq.login.finalizeMu,
	}
	lq.emailMu.Lock()
	lq.email = email
	lq.emailMu.Unlock()
	step = email.loginErrorStep("")
	step.Instructions = qrSessionRevokedInstructions
	return step, nil
}

func (lq *LineQRLogin) completeQR(ctx context.Context) (*bridgev2.LoginStep, error) {
	defer lq.login.Cancel()
	res, err := lq.client.QRCodeLoginV2(ctx, lq.session)
	if err != nil {
		return nil, wrapLineLoginError(err)
	}
	return lq.login.finishLogin(ctx, res)
}

func (lq *LineQRLogin) SubmitUserInput(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	email := lq.emailLogin()
	if email == nil {
		return nil, fmt.Errorf("QR login isn't waiting for input")
	}
	return email.SubmitUserInput(ctx, input)
}

func (lq *LineQRLogin) emailLogin() *LineEmailLogin {
	lq.emailMu.Lock()
	defer lq.emailMu.Unlock()
	return lq.email
}

func (lq *LineQRLogin) Cancel() {
	lq.login.Cancel()
	if email := lq.emailLogin(); email != nil {
		email.Cancel()
	}
}
