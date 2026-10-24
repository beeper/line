package line

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestGetRawChatRoundTripsThroughUpdateChat(t *testing.T) {
	// Member join times are larger than 2^53, so a float64 round trip would change them.
	const chatResponse = `{"code":0,"message":"ok","data":{"chats":[{"type":1,"chatMid":"c1","chatName":"Old","picturePath":"/old","extra":{"groupExtra":{"memberMids":{"u1":1729000000000123457}}}}]}}`
	var updateBody []byte
	client := NewClient("test-token")
	client.HTTPClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			response := `{"code":0,"message":"ok","data":null}`
			switch req.URL.Path {
			case "/api/talk/thrift/Talk/TalkService/getChats":
				response = chatResponse
			case "/api/talk/thrift/Talk/TalkService/updateChat":
				updateBody, _ = io.ReadAll(req.Body)
			default:
				t.Fatalf("unexpected request to %s", req.URL.Path)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(response)),
			}, nil
		}),
	}

	chat, err := client.GetRawChat(context.Background(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	chat["chatName"] = "New"
	if err = client.UpdateChat(context.Background(), 42, chat, ChatAttributeName); err != nil {
		t.Fatal(err)
	}

	if !bytes.Contains(updateBody, []byte(`"u1":1729000000000123457`)) {
		t.Fatalf("member join time was not kept exactly: %s", updateBody)
	}
	var args []struct {
		ReqSeq           int64          `json:"reqSeq"`
		Chat             map[string]any `json:"chat"`
		UpdatedAttribute int            `json:"updatedAttribute"`
	}
	if err = json.Unmarshal(updateBody, &args); err != nil {
		t.Fatal(err)
	}
	if len(args) != 1 {
		t.Fatalf("arg count = %d, want 1", len(args))
	}
	if args[0].ReqSeq != 42 || args[0].UpdatedAttribute != 1 {
		t.Fatalf("reqSeq/updatedAttribute = %d/%d, want 42/1", args[0].ReqSeq, args[0].UpdatedAttribute)
	}
	if args[0].Chat["chatName"] != "New" || args[0].Chat["picturePath"] != "/old" || args[0].Chat["chatMid"] != "c1" {
		t.Fatalf("chat = %#v", args[0].Chat)
	}
}

func TestGetRawChatMissingChat(t *testing.T) {
	client := newReactionTestClientWithResponse(t, "/api/talk/thrift/Talk/TalkService/getChats", `{"code":0,"data":{"chats":[]}}`, nil)
	if _, err := client.GetRawChat(context.Background(), "c1"); err == nil {
		t.Fatal("expected an error for a missing chat")
	}
}

func TestUpdateChatNonZeroCode(t *testing.T) {
	client := newReactionTestClientWithResponse(t, "/api/talk/thrift/Talk/TalkService/updateChat", `{"code":10051,"message":"TalkException"}`, nil)
	err := client.UpdateChat(context.Background(), 1, map[string]any{}, ChatAttributePicture)
	if err == nil || !strings.Contains(err.Error(), "updateChat failed") {
		t.Fatalf("err = %v, want updateChat failure", err)
	}
}

func TestUploadChatPictureRequest(t *testing.T) {
	jpegData := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x80, 0xff, 0xd9}
	client := NewClient("test-token")
	client.HTTPClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodPost || req.URL.Host != "line-chrome-gw.line-apps.com" || req.URL.Path != "/api/obs/uploadProfile" {
				t.Fatalf("request = %s %s", req.Method, req.URL)
			}
			if mid := req.URL.Query().Get("mid"); mid != "c1" {
				t.Fatalf("mid = %q, want c1", mid)
			}
			if contentType := req.Header.Get("Content-Type"); contentType != "image/jpeg" {
				t.Fatalf("Content-Type = %q, want image/jpeg", contentType)
			}
			if access := req.Header.Get("X-Line-Access"); access != "test-token" {
				t.Fatalf("X-Line-Access = %q, want test-token", access)
			}
			if req.Header.Get("X-Hmac") == "" {
				t.Fatal("X-Hmac header missing")
			}
			body, _ := io.ReadAll(req.Body)
			if !bytes.Equal(body, jpegData) {
				t.Fatalf("body = %x, want the raw JPEG bytes", body)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"code":0,"message":"ok","data":null}`)),
			}, nil
		}),
	}
	if err := client.UploadChatPicture(context.Background(), "c1", jpegData); err != nil {
		t.Fatal(err)
	}
}

func TestUploadChatPictureNonZeroCode(t *testing.T) {
	client := newReactionTestClientWithResponse(t, "/api/obs/uploadProfile", `{"code":1,"message":"failed"}`, nil)
	if err := client.UploadChatPicture(context.Background(), "c1", []byte{0xff, 0xd8}); err == nil {
		t.Fatal("expected an error for a non-zero code")
	}
}
