package line

import (
	"errors"
	"testing"
)

func TestIsRefreshRequired(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "talk exception code 119",
			err:  errors.New(`API error 400: {"code":10051,"message":"RESPONSE_ERROR","data":{"name":"TalkException","code":119,"reason":"Access token refresh required"}}`),
			want: true,
		},
		{
			name: "refresh text",
			err:  errors.New("Access token refresh required"),
			want: true,
		},
		{
			name: "refresh text lower case",
			err:  errors.New("access token refresh required"),
			want: true,
		},
		{
			name: "other talk exception",
			err:  errors.New(`API error 400: {"code":10051,"data":{"name":"TalkException","code":10,"reason":"not a member"}}`),
			want: false,
		},
		{
			name: "nil",
			err:  nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRefreshRequired(tt.err); got != tt.want {
				t.Fatalf("IsRefreshRequired() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsLoggedOut(t *testing.T) {
	if !IsLoggedOut(errors.New("V3_TOKEN_CLIENT_LOGGED_OUT")) {
		t.Fatal("expected logged-out error to be detected")
	}
	if !IsLoggedOut(errors.New(`API error 400: {"code":10051,"message":"RESPONSE_ERROR","data":{"name":"TalkException","message":"TalkException","code":83,"reason":"invalid sender key","parameterMap":null}}`)) {
		t.Fatal("expected invalid sender key to be detected as logged out")
	}
	if !IsLoggedOut(errors.New(`SSE error: 401: {"code":10004,"message":"REQUEST_NEED_LOGIN"}`)) {
		t.Fatal("expected request-need-login to be detected as logged out")
	}
	if !IsLoggedOut(errors.New(`SSE error: 401: {"code":10004}`)) {
		t.Fatal("expected request-need-login code to be detected as logged out")
	}
	if IsLoggedOut(errors.New(`SSE error: 401: {"code":100040}`)) {
		t.Fatal("similar longer numeric code should not be classified as logged out")
	}
	if IsLoggedOut(errors.New("Access token refresh required")) {
		t.Fatal("refresh-required error should not be classified as logged out")
	}
}

func TestIsUnauthorizedStatus(t *testing.T) {
	for _, err := range []error{
		errors.New("API error 401: unauthorized"),
		errors.New("API error 403: forbidden"),
		errors.New("HTTP 401: unauthorized"),
		errors.New("HTTP 403: forbidden"),
		errors.New("SSE error: 401"),
		errors.New("SSE error: 403"),
		errors.New("OBS upload failed (401): unauthorized"),
		errors.New("OBS upload failed (403): forbidden"),
		errors.New("OBS object info failed (401): unauthorized"),
		errors.New("OBS object info failed (403): forbidden"),
		errors.New("OBS download failed (401): unauthorized"),
		errors.New("OBS download failed (403): forbidden"),
		errors.New("api error 401: unauthorized"),
		errors.New("http 403: forbidden"),
		errors.New("sse ERROR: 401"),
		errors.New("obs DOWNLOAD failed (403): forbidden"),
	} {
		if !IsUnauthorizedStatus(err) {
			t.Fatalf("expected %q to be unauthorized", err)
		}
	}

	if IsUnauthorizedStatus(errors.New("HTTP 404: not found")) {
		t.Fatal("404 should not be classified as unauthorized")
	}
	for _, err := range []error{
		errors.New("request failed: dial tcp: i/o timeout"),
		errors.New("OBS upload request failed: connection reset by peer"),
		errors.New("OBS download request failed: context deadline exceeded"),
	} {
		if IsUnauthorizedStatus(err) {
			t.Fatalf("network error %q should not be classified as unauthorized", err)
		}
	}
}

func TestIsTalkExceptionNotFound(t *testing.T) {
	err := errors.New(`API error 400: {"code":10051,"message":"RESPONSE_ERROR","data":{"name":"TalkException","message":"TalkException","code":5,"reason":"not found","parameterMap":null}}`)
	if !IsTalkExceptionNotFound(err) {
		t.Fatal("expected TalkException code 5 not found to be detected")
	}
	if IsTalkExceptionNotFound(errors.New(`API error 400: {"code":10051,"message":"RESPONSE_ERROR","data":{"name":"TalkException","message":"TalkException","code":10,"reason":"not a member","parameterMap":null}}`)) {
		t.Fatal("not-a-member should not be classified as not-found")
	}
	if IsTalkExceptionNotFound(errors.New(`API error 400: {"code":10051,"message":"RESPONSE_ERROR","data":{"name":"TalkException","message":"TalkException","code":5,"reason":"different","parameterMap":null}}`)) {
		t.Fatal("code 5 with a different reason should not be classified as not-found")
	}
	if IsTalkExceptionNotFound(nil) {
		t.Fatal("nil should not be classified as not-found")
	}
}
