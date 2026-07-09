package line

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// sseHTTPClient is a dedicated HTTP client for SSE connections with no timeout.
// Reused across reconnects to avoid allocating a new transport pool each time.
var sseHTTPClient = &http.Client{}

var ErrSSEIdleTimeout = errors.New("SSE idle timeout")

const (
	maxSSEErrorBodyBytes  = 4096
	defaultSSEIdleTimeout = 150 * time.Second
)

var sseIdleTimeout = defaultSSEIdleTimeout

func IsSSEIdleTimeout(err error) bool {
	return errors.Is(err, ErrSSEIdleTimeout)
}

// ListenSSE connects to the Event Stream and blocks
func (c *Client) ListenSSE(ctx context.Context, localRev int64, callback func(event, data string)) error {
	q := url.Values{}
	q.Set("localRev", strconv.FormatInt(localRev, 10))
	q.Set("version", ExtensionVersion)
	q.Set("lastPartialFullSyncs", "{}")
	q.Set("language", "en_US")

	fullURL := fmt.Sprintf("https://line-chrome-gw.line-apps.com/api/operation/receive?%s", q.Encode())

	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		return err
	}

	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")
	if c.AccessToken != "" {
		req.Header.Set("x-line-access", c.AccessToken)
		req.Header.Set("Cookie", fmt.Sprintf("lct=%s", c.AccessToken))
	}

	resp, err := sseHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxSSEErrorBodyBytes))
		if bodyText := strings.TrimSpace(string(body)); bodyText != "" {
			return fmt.Errorf("SSE error: %d: %s", resp.StatusCode, bodyText)
		}
		return fmt.Errorf("SSE error: %d", resp.StatusCode)
	}

	reader := bufio.NewReader(resp.Body)

	var currentEvent string
	var dataLines []string

	flush := func() {
		if len(dataLines) == 0 && currentEvent == "" {
			return
		}
		data := strings.Join(dataLines, "\n")
		if data != "" && data != "null" {
			eventType := currentEvent
			if eventType == "" {
				eventType = "operation"
			}
			callback(eventType, data)
		}
		currentEvent = ""
		dataLines = dataLines[:0]
	}

	for {
		lineBytes, err := readSSELine(ctx, resp.Body, reader)
		if err != nil {
			return err
		}

		line := strings.TrimRight(string(lineBytes), "\r\n")
		if line == "" {
			flush()
			continue
		}

		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		field := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		switch field {
		case "event":
			currentEvent = value
		case "data":
			// data may be multi-line
			dataLines = append(dataLines, value)
		default:
			// ignore other fields for now
		}
	}
}

type sseReadResult struct {
	line []byte
	err  error
}

func readSSELine(ctx context.Context, body io.Closer, reader *bufio.Reader) ([]byte, error) {
	if sseIdleTimeout <= 0 {
		return reader.ReadBytes('\n')
	}

	resultCh := make(chan sseReadResult, 1)
	go func() {
		lineBytes, err := reader.ReadBytes('\n')
		resultCh <- sseReadResult{line: lineBytes, err: err}
	}()

	timer := time.NewTimer(sseIdleTimeout)
	defer timer.Stop()

	select {
	case result := <-resultCh:
		return result.line, result.err
	case <-timer.C:
		_ = body.Close()
		return nil, fmt.Errorf("%w after %s", ErrSSEIdleTimeout, sseIdleTimeout)
	case <-ctx.Done():
		_ = body.Close()
		return nil, ctx.Err()
	}
}
