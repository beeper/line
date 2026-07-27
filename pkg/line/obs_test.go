package line

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type observedOBSRequest struct {
	path    string
	query   string
	headers http.Header
}

func installCachedOBSToken(t *testing.T) {
	t.Helper()
	obsTokenMu.Lock()
	oldToken := obsTokenCache
	oldExpiry := obsTokenExpiry
	obsTokenCache = "obs-token"
	obsTokenExpiry = time.Now().Add(time.Hour)
	obsTokenMu.Unlock()
	t.Cleanup(func() {
		obsTokenMu.Lock()
		obsTokenCache = oldToken
		obsTokenExpiry = oldExpiry
		obsTokenMu.Unlock()
	})
}

func obsResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestDownloadOBSPlainMatchesChromeRequestFlow(t *testing.T) {
	installCachedOBSToken(t)

	var requests []observedOBSRequest
	client := NewClient("line-token")
	client.OBSClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			requests = append(requests, observedOBSRequest{
				path:    req.URL.Path,
				query:   req.URL.RawQuery,
				headers: req.Header.Clone(),
			})
			if len(requests) == 1 {
				return obsResponse(http.StatusOK, `{"status":"exist","encodeStatus":"done"}`), nil
			}
			return obsResponse(http.StatusOK, "image-bytes"), nil
		}),
	}

	data, err := client.DownloadOBSWithSIDOptions(context.Background(), "message-id", "", "m", OBSDownloadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "image-bytes" {
		t.Fatalf("data = %q, want image-bytes", data)
	}
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want object-info preflight plus download", len(requests))
	}
	if requests[0].path != "/r/talk/m/message-id/object_info.obs" || requests[1].path != "/r/talk/m/message-id" {
		t.Fatalf("request paths = %q / %q", requests[0].path, requests[1].path)
	}
	for i, req := range requests {
		if req.query != "" {
			t.Fatalf("request %d query = %q, want empty", i, req.query)
		}
		if req.headers.Get("X-Line-Access") != "obs-token" {
			t.Fatalf("request %d X-Line-Access = %q", i, req.headers.Get("X-Line-Access"))
		}
		if req.headers.Get("X-Line-Application") != lineApplicationHeader {
			t.Fatalf("request %d X-Line-Application = %q, want %q", i, req.headers.Get("X-Line-Application"), lineApplicationHeader)
		}
		if req.headers.Get("X-Talk-Meta") != "" {
			t.Fatalf("plain request %d unexpectedly sent X-Talk-Meta", i)
		}
	}
}

func TestDownloadOBSResourceUsesReceiveServiceAndSID(t *testing.T) {
	installCachedOBSToken(t)

	var requests []observedOBSRequest
	client := NewClient("line-token")
	client.OBSClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			requests = append(requests, observedOBSRequest{
				path:    req.URL.Path,
				headers: req.Header.Clone(),
			})
			if len(requests) == 1 {
				return obsResponse(http.StatusOK, `{"status":"exist","encodeStatus":"done"}`), nil
			}
			return obsResponse(http.StatusOK, "album-image"), nil
		}),
	}

	data, err := client.DownloadOBSResource(context.Background(), "album", "a", "preview-oid", "")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "album-image" {
		t.Fatalf("data = %q, want album-image", data)
	}
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want object-info preflight plus download", len(requests))
	}
	if requests[0].path != "/r/album/a/preview-oid/object_info.obs" || requests[1].path != "/r/album/a/preview-oid" {
		t.Fatalf("request paths = %q / %q", requests[0].path, requests[1].path)
	}
	for i, req := range requests {
		if req.headers.Get("X-Line-Access") != "obs-token" {
			t.Fatalf("request %d X-Line-Access = %q", i, req.headers.Get("X-Line-Access"))
		}
		if req.headers.Get("X-Line-Application") != lineApplicationHeader {
			t.Fatalf("request %d X-Line-Application = %q, want %q", i, req.headers.Get("X-Line-Application"), lineApplicationHeader)
		}
		if req.headers.Get("X-Talk-Meta") != "" {
			t.Fatalf("album request %d unexpectedly sent X-Talk-Meta", i)
		}
	}
}

func TestDownloadOBSPreflightsBaseObjectBeforeTID(t *testing.T) {
	installCachedOBSToken(t)

	var requests []observedOBSRequest
	client := NewClient("line-token")
	client.OBSClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			requests = append(requests, observedOBSRequest{path: req.URL.Path, query: req.URL.RawQuery})
			if len(requests) == 1 {
				return obsResponse(http.StatusOK, `{"status":"exist","encodeStatus":"done"}`), nil
			}
			return obsResponse(http.StatusOK, "image-bytes"), nil
		}),
	}

	_, err := client.DownloadOBSWithSIDOptions(context.Background(), "message-id", "", "m", OBSDownloadOptions{
		TID:    "original",
		OBSPop: "pop-value",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want object-info preflight plus download", len(requests))
	}
	if requests[0].path != "/r/talk/m/message-id/object_info.obs" {
		t.Fatalf("object-info path = %q, want base object path", requests[0].path)
	}
	if requests[1].path != "/r/talk/m/message-id/original" {
		t.Fatalf("download path = %q, want TID-specific path", requests[1].path)
	}
	for i, request := range requests {
		if request.query != "p=pop-value" {
			t.Fatalf("request %d query = %q, want OBS pop query", i, request.query)
		}
	}
}

func TestDownloadOBSEncryptedIncludesTalkMeta(t *testing.T) {
	installCachedOBSToken(t)

	var talkMetaHeaders []string
	client := NewClient("line-token")
	client.OBSClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			talkMetaHeaders = append(talkMetaHeaders, req.Header.Get("X-Talk-Meta"))
			if len(talkMetaHeaders) == 1 {
				return obsResponse(http.StatusOK, `{"status":"exist","encodeStatus":"done"}`), nil
			}
			return obsResponse(http.StatusOK, "encrypted-image-bytes"), nil
		}),
	}

	_, err := client.DownloadOBSWithOptions(context.Background(), "message-id", "message-id", OBSDownloadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(talkMetaHeaders) != 2 {
		t.Fatalf("requests = %d, want object-info preflight plus download", len(talkMetaHeaders))
	}
	for i, talkMeta := range talkMetaHeaders {
		if talkMeta == "" {
			t.Fatalf("encrypted request %d omitted X-Talk-Meta", i)
		}
	}
}

func TestDownloadOBSWaitsForObjectEncoding(t *testing.T) {
	installCachedOBSToken(t)
	oldDelay := obsRetryDelay
	obsRetryDelay = 0
	t.Cleanup(func() { obsRetryDelay = oldDelay })

	var paths []string
	client := NewClient("line-token")
	client.OBSClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			paths = append(paths, req.URL.Path)
			switch len(paths) {
			case 1:
				return obsResponse(http.StatusOK, `{"status":"exist","encodeStatus":"ing"}`), nil
			case 2:
				return obsResponse(http.StatusOK, `{"status":"exist","encodeStatus":"done"}`), nil
			default:
				return obsResponse(http.StatusOK, "ready"), nil
			}
		}),
	}

	data, err := client.DownloadOBSWithSIDOptions(context.Background(), "message-id", "", "m", OBSDownloadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "ready" {
		t.Fatalf("data = %q, want ready", data)
	}
	want := []string{
		"/r/talk/m/message-id/object_info.obs",
		"/r/talk/m/message-id/object_info.obs",
		"/r/talk/m/message-id",
	}
	if strings.Join(paths, "\n") != strings.Join(want, "\n") {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
}

func TestDownloadOBSClassifiesMissingObject(t *testing.T) {
	installCachedOBSToken(t)

	client := NewClient("line-token")
	client.OBSClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return obsResponse(http.StatusOK, `{"status":"notexist"}`), nil
		}),
	}

	_, err := client.DownloadOBSWithSIDOptions(context.Background(), "message-id", "", "m", OBSDownloadOptions{})
	if !errors.Is(err, ErrOBSObjectNotFound) {
		t.Fatalf("err = %v, want ErrOBSObjectNotFound", err)
	}
}

func TestDownloadOBSDoesNotCallObjectInfoHTTP404Expired(t *testing.T) {
	installCachedOBSToken(t)

	client := NewClient("line-token")
	client.OBSClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return obsResponse(http.StatusNotFound, "not found"), nil
		}),
	}

	_, err := client.DownloadOBSWithSIDOptions(context.Background(), "message-id", "", "m", OBSDownloadOptions{})
	if err == nil {
		t.Fatal("expected object-info request error")
	}
	if errors.Is(err, ErrOBSObjectNotFound) {
		t.Fatalf("err = %v, raw HTTP failure must not be materialized as known expiry", err)
	}
}

func TestDownloadOBSClassifiesObjectInfoUnauthorized(t *testing.T) {
	installCachedOBSToken(t)

	client := NewClient("line-token")
	client.OBSClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return obsResponse(http.StatusUnauthorized, "unauthorized"), nil
		}),
	}

	_, err := client.DownloadOBSWithSIDOptions(context.Background(), "message-id", "", "m", OBSDownloadOptions{})
	if !IsUnauthorizedStatus(err) {
		t.Fatalf("err = %v, want unauthorized status for token recovery", err)
	}
}

func TestDownloadOBSDoesNotCallPostPreflight404Expired(t *testing.T) {
	installCachedOBSToken(t)

	var requests int
	client := NewClient("line-token")
	client.OBSClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			requests++
			if requests == 1 {
				return obsResponse(http.StatusOK, `{"status":"exist","encodeStatus":"done"}`), nil
			}
			return obsResponse(http.StatusNotFound, "not found"), nil
		}),
	}

	_, err := client.DownloadOBSWithSIDOptions(context.Background(), "message-id", "", "m", OBSDownloadOptions{})
	if err == nil {
		t.Fatal("expected download error")
	}
	if errors.Is(err, ErrOBSObjectNotFound) {
		t.Fatalf("err = %v, post-preflight race must not be materialized as known expiry", err)
	}
}

func TestDownloadOBSClassifiesEncodingRetryExhaustion(t *testing.T) {
	installCachedOBSToken(t)
	oldDelay := obsRetryDelay
	obsRetryDelay = 0
	t.Cleanup(func() { obsRetryDelay = oldDelay })

	var requests int
	client := NewClient("line-token")
	client.OBSClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			requests++
			return obsResponse(http.StatusOK, `{"status":"exist","encodeStatus":"ing"}`), nil
		}),
	}

	_, err := client.DownloadOBSWithSIDOptions(context.Background(), "message-id", "", "m", OBSDownloadOptions{})
	if !errors.Is(err, ErrOBSEncodingIncomplete) {
		t.Fatalf("err = %v, want ErrOBSEncodingIncomplete", err)
	}
	if requests != obsMaxRetries+1 {
		t.Fatalf("requests = %d, want %d", requests, obsMaxRetries+1)
	}
}

func TestDownloadAlbumPreviewMatchesLINERequestContract(t *testing.T) {
	var channelRequests int
	client := NewClient("line-token")
	client.HTTPClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			channelRequests++
			if req.URL.Path != "/api/talk/thrift/Talk/ChannelService/issueChannelToken" {
				t.Fatalf("channel token path = %q", req.URL.Path)
			}
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != `["1341209850"]` {
				t.Fatalf("channel token body = %q", body)
			}
			return obsResponse(
				http.StatusOK,
				`{"code":0,"message":"OK","data":{"channelAccessToken":"channel-token","expiration":9999999999999}}`,
			), nil
		}),
	}

	var requests []observedOBSRequest
	client.OBSClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			requests = append(requests, observedOBSRequest{
				path:    req.URL.Path,
				query:   req.URL.RawQuery,
				headers: req.Header.Clone(),
			})
			return obsResponse(http.StatusOK, "album-thumbnail"), nil
		}),
	}

	for range 2 {
		data, err := client.DownloadAlbumPreview(
			context.Background(),
			"preview-oid",
			"chat-id",
			"album-id",
		)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "album-thumbnail" {
			t.Fatalf("data = %q, want album-thumbnail", data)
		}
	}

	if channelRequests != 1 {
		t.Fatalf("channel token requests = %d, want one cached request", channelRequests)
	}
	if len(requests) != 2 {
		t.Fatalf("OBS requests = %d, want two direct thumbnail downloads", len(requests))
	}
	for index, request := range requests {
		if request.path != "/r/album/a/preview-oid/f482x482" {
			t.Fatalf("request %d path = %q", index, request.path)
		}
		if request.query != "" {
			t.Fatalf("request %d query = %q, want empty", index, request.query)
		}
		if request.headers.Get("X-Line-ChannelToken") != "channel-token" {
			t.Fatalf("request %d channel token = %q", index, request.headers.Get("X-Line-ChannelToken"))
		}
		if request.headers.Get("X-Line-Mid") != "chat-id" {
			t.Fatalf("request %d MID = %q", index, request.headers.Get("X-Line-Mid"))
		}
		if request.headers.Get("X-Line-Album") != "album-id" {
			t.Fatalf("request %d album = %q", index, request.headers.Get("X-Line-Album"))
		}
		if request.headers.Get("X-Line-Access") != "line-token" {
			t.Fatalf("request %d access token = %q", index, request.headers.Get("X-Line-Access"))
		}
		if request.headers.Get("User-Agent") == "" {
			t.Fatalf("request %d omitted User-Agent", index)
		}
		if request.headers.Get("X-Line-Application") != "" {
			t.Fatalf("request %d unexpectedly sent X-Line-Application", index)
		}
	}
}

func TestDownloadAlbumPreviewClassifiesHTTPStatus(t *testing.T) {
	tests := []struct {
		name   string
		status int
		check  func(error) bool
	}{
		{
			name:   "encoding incomplete",
			status: http.StatusAccepted,
			check:  func(err error) bool { return errors.Is(err, ErrOBSEncodingIncomplete) },
		},
		{
			name:   "missing object",
			status: http.StatusNotFound,
			check:  func(err error) bool { return errors.Is(err, ErrOBSObjectNotFound) },
		},
		{
			name:   "unauthorized",
			status: http.StatusForbidden,
			check:  IsUnauthorizedStatus,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := NewClient("line-token")
			client.channelTokenCache = map[string]cachedChannelAccessToken{
				albumPreviewChannelID: {
					token:     "channel-token",
					expiresAt: time.Now().Add(time.Hour),
				},
			}
			client.OBSClient = &http.Client{
				Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					return obsResponse(test.status, "error"), nil
				}),
			}

			_, err := client.DownloadAlbumPreview(
				context.Background(),
				"preview-oid",
				"chat-id",
				"",
			)
			if !test.check(err) {
				t.Fatalf("err = %v, wrong classification", err)
			}
		})
	}
}
