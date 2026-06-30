package line

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func newReactionTestClient(t *testing.T, wantPath string, body *string) *Client {
	return newReactionTestClientWithResponse(t, wantPath, `{"code":0,"message":"ok","data":null}`, body)
}

func newReactionTestClientWithResponse(t *testing.T, wantPath, responseBody string, body *string) *Client {
	t.Helper()
	client := NewClient("test-token")
	client.HTTPClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path != wantPath {
				t.Fatalf("path = %q, want %q", req.URL.Path, wantPath)
			}
			data, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatal(err)
			}
			if body != nil {
				*body = string(data)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(responseBody)),
			}, nil
		}),
	}
	return client
}

func TestReactRequestBody(t *testing.T) {
	var body string
	client := newReactionTestClient(t, "/api/talk/thrift/Talk/TalkService/react", &body)
	err := client.React(123, "616934195205767730", ReactionType{
		PaidReactionType: &PaidReactionType{
			ProductID:    "670e0cce840a8236ddd4ee4c",
			EmojiID:      "211",
			ResourceType: 1,
			Version:      1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var args []ReactRequest
	if err = json.Unmarshal([]byte(body), &args); err != nil {
		t.Fatal(err)
	}
	if len(args) != 1 {
		t.Fatalf("arg count = %d, want 1", len(args))
	}
	req := args[0]
	if req.ReqSeq != 123 || req.MessageID != "616934195205767730" {
		t.Fatalf("request seq/message = %d/%s, want 123/616934195205767730", req.ReqSeq, req.MessageID)
	}
	if req.ReactionType.PaidReactionType == nil {
		t.Fatal("paidReactionType missing")
	}
	if req.ReactionType.PaidReactionType.ProductID != "670e0cce840a8236ddd4ee4c" ||
		req.ReactionType.PaidReactionType.EmojiID != "211" ||
		req.ReactionType.PaidReactionType.ResourceType != 1 ||
		req.ReactionType.PaidReactionType.Version != 1 {
		t.Fatalf("paidReactionType = %#v", req.ReactionType.PaidReactionType)
	}
}

func TestCancelReactionRequestBody(t *testing.T) {
	var body string
	client := newReactionTestClient(t, "/api/talk/thrift/Talk/TalkService/cancelReaction", &body)
	err := client.CancelReaction(123, "616934195205767730")
	if err != nil {
		t.Fatal(err)
	}

	var args []CancelReactionRequest
	if err = json.Unmarshal([]byte(body), &args); err != nil {
		t.Fatal(err)
	}
	if len(args) != 1 {
		t.Fatalf("arg count = %d, want 1", len(args))
	}
	if args[0].ReqSeq != 123 || args[0].MessageID != "616934195205767730" {
		t.Fatalf("request seq/message = %d/%s, want 123/616934195205767730", args[0].ReqSeq, args[0].MessageID)
	}
}

func TestReactNonZeroWrapperKeepsInvalidPaidReactionDetails(t *testing.T) {
	client := newReactionTestClientWithResponse(t, "/api/talk/thrift/Talk/TalkService/react", `{"code":10051,"message":"RESPONSE_ERROR","data":{"name":"TalkException","message":"TalkException","code":0,"reason":"Invalid paidReactionType in reactionType","parameterMap":null}}`, nil)
	err := client.React(123, "616934195205767730", ReactionType{
		PaidReactionType: &PaidReactionType{
			ProductID:    "670e0cce840a8236ddd4ee4c",
			EmojiID:      "211",
			ResourceType: 1,
			Version:      1,
		},
	})
	if err == nil {
		t.Fatal("expected non-zero wrapper error")
	}
	if !IsInvalidPaidReactionType(err) {
		t.Fatalf("expected invalid paid reaction error to be detected from %q", err.Error())
	}
}

func TestIsInvalidPaidReactionType(t *testing.T) {
	err := errors.New(`API error 400: {"code":10051,"message":"RESPONSE_ERROR","data":{"name":"TalkException","message":"TalkException","code":0,"reason":"Invalid paidReactionType in reactionType","parameterMap":null}}`)
	if !IsInvalidPaidReactionType(err) {
		t.Fatal("expected invalid paid reaction error to be detected")
	}
	if IsInvalidPaidReactionType(errors.New("other error")) {
		t.Fatal("unexpected invalid paid reaction match")
	}
}

func TestIsNotAMemberError(t *testing.T) {
	err := errors.New(`API error 400: {"code":10051,"message":"RESPONSE_ERROR","data":{"name":"TalkException","message":"TalkException","code":10,"reason":"You are not a member of this chat","parameterMap":null}}`)
	if !IsNotAMemberError(err) {
		t.Fatal("expected not-a-member error to be detected")
	}
	if IsNotAMemberError(errors.New("other error")) {
		t.Fatal("unexpected not-a-member match")
	}
}
