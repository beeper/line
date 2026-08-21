package connector

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"

	"github.com/highesttt/matrix-line-messenger/pkg/line"
)

func newPeerKeyTestClient() *LineClient {
	return &LineClient{
		AccessToken: "access",
		UserLogin: &bridgev2.UserLogin{
			Bridge: &bridgev2.Bridge{Log: zerolog.New(io.Discard)},
		},
	}
}

func TestEnsurePeerKeyRecoversAndRetriesRefreshRequired(t *testing.T) {
	oldNewClient := newLineAPIClient
	oldNegotiate := negotiateE2EEPublicKeyWithClient
	oldRecover := recoverLineToken
	t.Cleanup(func() {
		newLineAPIClient = oldNewClient
		negotiateE2EEPublicKeyWithClient = oldNegotiate
		recoverLineToken = oldRecover
	})

	var newClientCalls int
	newLineAPIClient = func(token string) *line.Client {
		newClientCalls++
		return line.NewClient(token)
	}

	var negotiateCalls int
	negotiateE2EEPublicKeyWithClient = func(*line.Client, string) (*line.E2EEPublicKey, error) {
		negotiateCalls++
		if negotiateCalls == 1 {
			return nil, errAuthRequired
		}
		return &line.E2EEPublicKey{
			KeyID:     json.Number("42"),
			PublicKey: "peer-public-key",
		}, nil
	}

	var recoverCalls int
	recoverLineToken = func(*LineClient, context.Context) error {
		recoverCalls++
		return nil
	}

	lc := newPeerKeyTestClient()
	keyID, publicKey, err := lc.ensurePeerKey(context.Background(), "peer-mid")
	if err != nil {
		t.Fatalf("ensurePeerKey returned error: %v", err)
	}
	if keyID != 42 || publicKey != "peer-public-key" {
		t.Fatalf("ensurePeerKey returned keyID=%d publicKey=%q", keyID, publicKey)
	}
	if negotiateCalls != 2 {
		t.Fatalf("negotiate calls = %d, want 2", negotiateCalls)
	}
	if recoverCalls != 1 {
		t.Fatalf("recovery calls = %d, want 1", recoverCalls)
	}
	if newClientCalls != 2 {
		t.Fatalf("new clients = %d, want 2", newClientCalls)
	}
}

func TestEnsurePeerKeyByIDRecoversAndRetriesRefreshRequired(t *testing.T) {
	oldNewClient := newLineAPIClient
	oldGetKey := getE2EEPublicKeyWithClient
	oldRecover := recoverLineToken
	t.Cleanup(func() {
		newLineAPIClient = oldNewClient
		getE2EEPublicKeyWithClient = oldGetKey
		recoverLineToken = oldRecover
	})

	var newClientCalls int
	newLineAPIClient = func(token string) *line.Client {
		newClientCalls++
		return line.NewClient(token)
	}

	var getKeyCalls int
	getE2EEPublicKeyWithClient = func(*line.Client, string, int, int) (*line.E2EEPublicKey, error) {
		getKeyCalls++
		if getKeyCalls == 1 {
			return nil, errAuthRequired
		}
		return &line.E2EEPublicKey{
			KeyID:     json.Number("5910969"),
			PublicKey: "specific-peer-public-key",
		}, nil
	}

	var recoverCalls int
	recoverLineToken = func(*LineClient, context.Context) error {
		recoverCalls++
		return nil
	}

	lc := newPeerKeyTestClient()
	keyID, publicKey, err := lc.ensurePeerKeyByID(context.Background(), "peer-mid", 5910969)
	if err != nil {
		t.Fatalf("ensurePeerKeyByID returned error: %v", err)
	}
	if keyID != 5910969 || publicKey != "specific-peer-public-key" {
		t.Fatalf("ensurePeerKeyByID returned keyID=%d publicKey=%q", keyID, publicKey)
	}
	if getKeyCalls != 2 {
		t.Fatalf("get key calls = %d, want 2", getKeyCalls)
	}
	if recoverCalls != 1 {
		t.Fatalf("recovery calls = %d, want 1", recoverCalls)
	}
	if newClientCalls != 2 {
		t.Fatalf("new clients = %d, want 2", newClientCalls)
	}
}

func TestEnsurePeerKeyCachesNoUsablePublicKeyWithoutRecovery(t *testing.T) {
	oldNegotiate := negotiateE2EEPublicKeyWithClient
	oldRecover := recoverLineToken
	t.Cleanup(func() {
		negotiateE2EEPublicKeyWithClient = oldNegotiate
		recoverLineToken = oldRecover
	})

	var negotiateCalls int
	negotiateE2EEPublicKeyWithClient = func(*line.Client, string) (*line.E2EEPublicKey, error) {
		negotiateCalls++
		return nil, line.ErrNoUsableE2EEPublicKey
	}
	recoverLineToken = func(*LineClient, context.Context) error {
		t.Fatal("recovery should not be called for no-usable-public-key errors")
		return nil
	}

	lc := newPeerKeyTestClient()
	_, _, err := lc.ensurePeerKey(context.Background(), "peer-mid")
	if !errors.Is(err, line.ErrNoUsableE2EEPublicKey) {
		t.Fatalf("ensurePeerKey error = %v, want ErrNoUsableE2EEPublicKey", err)
	}

	_, _, err = lc.ensurePeerKey(context.Background(), "peer-mid")
	if !errors.Is(err, line.ErrNoUsableE2EEPublicKey) {
		t.Fatalf("cached ensurePeerKey error = %v, want ErrNoUsableE2EEPublicKey", err)
	}
	if negotiateCalls != 1 {
		t.Fatalf("negotiate calls = %d, want cached negative lookup", negotiateCalls)
	}
}

func TestGroupKeyMemberMIDsKeepsOnlyUniqueUsers(t *testing.T) {
	lc := newPeerKeyTestClient()
	lc.Mid = "U-self"

	got := lc.groupKeyMemberMIDs("C-group", []string{
		"U-self",
		"C-group",
		"R-room",
		"U-peer",
		"U-peer",
		"u-lowercase-peer",
		"",
		"not-a-mid",
	})

	if len(got) != 2 || got[0] != "U-peer" || got[1] != "u-lowercase-peer" {
		t.Fatalf("groupKeyMemberMIDs = %v, want [U-peer u-lowercase-peer]", got)
	}
}

func TestIsUserMIDAcceptsWirePrefixCasing(t *testing.T) {
	tests := map[string]bool{
		"U-peer": true,
		"u-peer": true,
		"C-chat": false,
		"R-room": false,
		"":       false,
		"U":      false,
	}
	for mid, want := range tests {
		if got := isUserMID(mid); got != want {
			t.Errorf("isUserMID(%q) = %t, want %t", mid, got, want)
		}
	}
}

func TestCacheGroupMemberMIDsPreservesRicherList(t *testing.T) {
	lc := newPeerKeyTestClient()
	lc.groupMemberCache = map[string][]string{
		"C-group": {"U-self", "U-peer"},
	}

	lc.cacheGroupMemberMIDs("C-group", []string{"U-self"})

	got := lc.groupMemberCache["C-group"]
	if len(got) != 2 || got[0] != "U-self" || got[1] != "U-peer" {
		t.Fatalf("cached members = %v, want richer prior list", got)
	}
}

func TestGroupKeyFetchErrorAllowsLatestKeyPlaintextFallback(t *testing.T) {
	err := groupKeyFetchError(0, errNotMember)

	if !errors.Is(err, line.ErrNoUsableE2EEGroupKey) {
		t.Fatalf("error = %v, want ErrNoUsableE2EEGroupKey", err)
	}
	if !errors.Is(err, errNotMember) {
		t.Fatalf("error = %v, want not-member error to remain wrapped", err)
	}
}

func TestGroupKeyFetchErrorPreservesSpecificKeyMembershipError(t *testing.T) {
	err := groupKeyFetchError(123, errNotMember)

	if !errors.Is(err, errNotMember) {
		t.Fatalf("error = %v, want original not-member error", err)
	}
	if errors.Is(err, line.ErrNoUsableE2EEGroupKey) {
		t.Fatalf("error = %v, specific group key lookup must not allow plaintext fallback", err)
	}
}

func TestRegisterGroupKeyAllowsPlaintextFallbackWithoutKnownMembers(t *testing.T) {
	lc := newPeerKeyTestClient()
	lc.Mid = "U-self"

	err := lc.registerGroupKey(context.Background(), "C-group", []string{"U-self"})
	if !errors.Is(err, line.ErrNoUsableE2EEGroupKey) {
		t.Fatalf("error = %v, want ErrNoUsableE2EEGroupKey", err)
	}
}

func TestJoinedGroupMemberMIDsExcludeInvitees(t *testing.T) {
	group := &line.GroupExtra{
		MemberMids: line.FlexibleMidMap{
			"U-self":   true,
			"U-member": true,
		},
		InviteeMids: line.FlexibleMidMap{
			"U-invitee": true,
		},
	}

	mids, hasPendingInvitees := joinedGroupMemberMIDs(group, "U-self")
	if !hasPendingInvitees {
		t.Fatal("hasPendingInvitees = false, want true")
	}
	if len(mids) != 2 {
		t.Fatalf("joined mids = %v, want self and joined member only", mids)
	}
	for _, mid := range mids {
		if mid == "U-invitee" {
			t.Fatalf("joined mids unexpectedly contain pending invitee: %v", mids)
		}
	}
}

func TestResolveGroupMemberPublicKeysFillsPartialBatchResponse(t *testing.T) {
	oldGetLast := getLastE2EEPublicKeysWithClient
	oldNegotiate := negotiateE2EEPublicKeyWithClient
	t.Cleanup(func() {
		getLastE2EEPublicKeysWithClient = oldGetLast
		negotiateE2EEPublicKeyWithClient = oldNegotiate
	})

	getLastE2EEPublicKeysWithClient = func(*line.Client, line.GetLastE2EEPublicKeysRequest) (map[string]line.E2EEPeerPublicKey, error) {
		return map[string]line.E2EEPeerPublicKey{
			"U-batch": {KeyID: 10, KeyData: "batch-public-key"},
		}, nil
	}
	var negotiated []string
	negotiateE2EEPublicKeyWithClient = func(_ *line.Client, mid string) (*line.E2EEPublicKey, error) {
		negotiated = append(negotiated, mid)
		return &line.E2EEPublicKey{KeyID: json.Number("20"), PublicKey: "negotiated-public-key"}, nil
	}

	lc := newPeerKeyTestClient()
	_, got, err := lc.resolveGroupMemberPublicKeys(
		context.Background(),
		line.NewClient("access"),
		"C-group",
		[]string{"U-batch", "U-missing"},
	)
	if err != nil {
		t.Fatalf("resolveGroupMemberPublicKeys returned error: %v", err)
	}
	if len(negotiated) != 1 || negotiated[0] != "U-missing" {
		t.Fatalf("negotiated members = %v, want [U-missing]", negotiated)
	}
	if got["U-batch"].KeyID != 10 || got["U-missing"].KeyID != 20 {
		t.Fatalf("resolved keys = %#v, want batch and negotiated entries", got)
	}
}

func TestResolveGroupMemberPublicKeysFallsBackAfterBatchError(t *testing.T) {
	oldGetLast := getLastE2EEPublicKeysWithClient
	oldNegotiate := negotiateE2EEPublicKeyWithClient
	t.Cleanup(func() {
		getLastE2EEPublicKeysWithClient = oldGetLast
		negotiateE2EEPublicKeyWithClient = oldNegotiate
	})

	getLastE2EEPublicKeysWithClient = func(*line.Client, line.GetLastE2EEPublicKeysRequest) (map[string]line.E2EEPeerPublicKey, error) {
		return nil, errors.New("batch unavailable")
	}
	var negotiated []string
	negotiateE2EEPublicKeyWithClient = func(_ *line.Client, mid string) (*line.E2EEPublicKey, error) {
		negotiated = append(negotiated, mid)
		return &line.E2EEPublicKey{KeyID: json.Number("30"), PublicKey: "fallback-public-key"}, nil
	}

	lc := newPeerKeyTestClient()
	_, got, err := lc.resolveGroupMemberPublicKeys(
		context.Background(),
		line.NewClient("access"),
		"C-group",
		[]string{"U-peer"},
	)
	if err != nil {
		t.Fatalf("resolveGroupMemberPublicKeys returned error: %v", err)
	}
	if len(negotiated) != 1 || negotiated[0] != "U-peer" {
		t.Fatalf("negotiated members = %v, want [U-peer]", negotiated)
	}
	if got["U-peer"].KeyID != 30 {
		t.Fatalf("resolved key = %#v, want fallback key", got["U-peer"])
	}
}

func TestResolveGroupMemberPublicKeysReturnsNoUsableGroupKey(t *testing.T) {
	oldGetLast := getLastE2EEPublicKeysWithClient
	oldNegotiate := negotiateE2EEPublicKeyWithClient
	t.Cleanup(func() {
		getLastE2EEPublicKeysWithClient = oldGetLast
		negotiateE2EEPublicKeyWithClient = oldNegotiate
	})

	getLastE2EEPublicKeysWithClient = func(*line.Client, line.GetLastE2EEPublicKeysRequest) (map[string]line.E2EEPeerPublicKey, error) {
		return nil, nil
	}
	negotiateE2EEPublicKeyWithClient = func(*line.Client, string) (*line.E2EEPublicKey, error) {
		return nil, line.ErrNoUsableE2EEPublicKey
	}

	lc := newPeerKeyTestClient()
	_, _, err := lc.resolveGroupMemberPublicKeys(
		context.Background(),
		line.NewClient("access"),
		"C-group",
		[]string{"U-peer"},
	)
	if !errors.Is(err, line.ErrNoUsableE2EEGroupKey) {
		t.Fatalf("error = %v, want ErrNoUsableE2EEGroupKey", err)
	}
}

func TestResolveGroupMemberPublicKeysAllowsFallbackAfterMemberError(t *testing.T) {
	oldGetLast := getLastE2EEPublicKeysWithClient
	oldNegotiate := negotiateE2EEPublicKeyWithClient
	t.Cleanup(func() {
		getLastE2EEPublicKeysWithClient = oldGetLast
		negotiateE2EEPublicKeyWithClient = oldNegotiate
	})

	getLastE2EEPublicKeysWithClient = func(*line.Client, line.GetLastE2EEPublicKeysRequest) (map[string]line.E2EEPeerPublicKey, error) {
		return nil, nil
	}
	memberErr := errors.New("temporary member key failure")
	negotiateE2EEPublicKeyWithClient = func(*line.Client, string) (*line.E2EEPublicKey, error) {
		return nil, memberErr
	}

	lc := newPeerKeyTestClient()
	_, _, err := lc.resolveGroupMemberPublicKeys(
		context.Background(),
		line.NewClient("access"),
		"C-group",
		[]string{"U-peer"},
	)
	if !errors.Is(err, line.ErrNoUsableE2EEGroupKey) {
		t.Fatalf("error = %v, want ErrNoUsableE2EEGroupKey", err)
	}
	if !errors.Is(err, memberErr) {
		t.Fatalf("error = %v, want member error to remain wrapped", err)
	}
}

func TestResolveGroupMemberPublicKeysPreservesAuthRecoveryFailure(t *testing.T) {
	oldGetLast := getLastE2EEPublicKeysWithClient
	oldNegotiate := negotiateE2EEPublicKeyWithClient
	oldRecover := recoverLineToken
	t.Cleanup(func() {
		getLastE2EEPublicKeysWithClient = oldGetLast
		negotiateE2EEPublicKeyWithClient = oldNegotiate
		recoverLineToken = oldRecover
	})

	getLastE2EEPublicKeysWithClient = func(*line.Client, line.GetLastE2EEPublicKeysRequest) (map[string]line.E2EEPeerPublicKey, error) {
		return nil, nil
	}
	negotiateE2EEPublicKeyWithClient = func(*line.Client, string) (*line.E2EEPublicKey, error) {
		return nil, errAuthRequired
	}
	recoveryErr := errors.New("token recovery failed")
	recoverLineToken = func(*LineClient, context.Context) error {
		return recoveryErr
	}

	lc := newPeerKeyTestClient()
	_, _, err := lc.resolveGroupMemberPublicKeys(
		context.Background(),
		line.NewClient("access"),
		"C-group",
		[]string{"U-peer"},
	)
	if !errors.Is(err, errAuthRequired) || !errors.Is(err, recoveryErr) {
		t.Fatalf("error = %v, want auth and recovery failures", err)
	}
	if errors.Is(err, line.ErrNoUsableE2EEGroupKey) {
		t.Fatalf("error = %v, auth failure must not allow plaintext fallback", err)
	}
}
