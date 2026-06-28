package line

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type rewriteTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (rt rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	cloned.URL.Scheme = rt.target.Scheme
	cloned.URL.Host = rt.target.Host
	return rt.base.RoundTrip(cloned)
}

func setCachedOBSToken(t *testing.T) {
	t.Helper()

	obsTokenMu.Lock()
	oldToken := obsTokenCache
	oldExpiry := obsTokenExpiry
	obsTokenCache = "cached-obs-token"
	obsTokenExpiry = time.Now().Add(time.Hour)
	obsTokenMu.Unlock()

	t.Cleanup(func() {
		obsTokenMu.Lock()
		obsTokenCache = oldToken
		obsTokenExpiry = oldExpiry
		obsTokenMu.Unlock()
	})
}

func testOBSClient(t *testing.T, handler http.Handler) (*Client, *atomic.Int32) {
	t.Helper()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)

	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}

	return &Client{
		AccessToken: "line-token",
		OBSClient: &http.Client{
			Transport: rewriteTransport{target: target, base: http.DefaultTransport},
		},
		obsRetryDelay: time.Nanosecond,
	}, &requests
}

func TestDownloadOBSWithSIDOptionsRetriesWhileMediaIsProcessing(t *testing.T) {
	setCachedOBSToken(t)

	var processingResponses atomic.Int32
	client, requests := testOBSClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/r/talk/m/oid/original" {
			t.Errorf("unexpected OBS path: %s", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusBadRequest)
			return
		}
		if r.Header.Get("x-line-access") != "cached-obs-token" {
			t.Errorf("missing cached OBS token header")
			http.Error(w, "missing token", http.StatusBadRequest)
			return
		}
		if processingResponses.Add(1) <= 6 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		_, _ = io.WriteString(w, "image-bytes")
	}))

	data, err := client.DownloadOBSWithSIDOptions(context.Background(), "oid", "message-id", "m", OBSDownloadOptions{
		TID:                  "original",
		MaxProcessingRetries: 10,
	})
	if err != nil {
		t.Fatalf("DownloadOBSWithSIDOptions returned error: %v", err)
	}
	if string(data) != "image-bytes" {
		t.Fatalf("unexpected OBS response data: %q", data)
	}
	if requests.Load() != 7 {
		t.Fatalf("expected 7 OBS requests, got %d", requests.Load())
	}
}

func TestDownloadOBSWithSIDOptionsUsesDefaultProcessingRetryLimit(t *testing.T) {
	setCachedOBSToken(t)

	client, requests := testOBSClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))

	_, err := client.DownloadOBSWithSIDOptions(context.Background(), "oid", "message-id", "m", OBSDownloadOptions{})
	if err == nil {
		t.Fatal("expected OBS download error")
	}
	if !strings.Contains(err.Error(), "media still processing after 5 retries") {
		t.Fatalf("unexpected error: %v", err)
	}
	if requests.Load() != 6 {
		t.Fatalf("expected 6 OBS requests, got %d", requests.Load())
	}
}

func TestDownloadOBSWithSIDOptionsDoesNotRetryOtherStatusCodes(t *testing.T) {
	setCachedOBSToken(t)

	client, requests := testOBSClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "boom")
	}))

	_, err := client.DownloadOBSWithSIDOptions(context.Background(), "oid", "message-id", "m", OBSDownloadOptions{})
	if err == nil {
		t.Fatal("expected OBS download error")
	}
	if !strings.Contains(err.Error(), "OBS download failed (500): boom") {
		t.Fatalf("unexpected error: %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("expected 1 OBS request, got %d", requests.Load())
	}
}
