// Package todoist is a minimal client for the Todoist API v1.
//
// Only api.todoist.com/api/v1 exists; /rest/v2 and /sync/v9 return 410 Gone and
// are deliberately unsupported.
//
// # Creating tasks
//
// Tasks are created through [Client.Sync] rather than a REST endpoint, because
// only the /sync commands carry a uuid that Todoist treats as an idempotency
// key: it refuses to execute a command whose uuid already ran. Deriving that
// uuid deterministically from a stable identifier — see [CommandUUID] — makes
// re-running a push a server-side no-op instead of a source of duplicates.
//
//	client := todoist.New(todoist.DefaultBaseURL, token)
//	cmd := todoist.BuildItemAdd(todoist.IssueInput{
//		URL:   "https://github.com/owner/repo/issues/812",
//		Ref:   "owner/repo#812",
//		Title: "fix session token rotation",
//	}, projectID, "gh")
//
//	res, err := client.Sync(ctx, []todoist.Command{cmd})
//	if err != nil {
//		return err
//	}
//	if !res.OK(cmd.UUID) {
//		return fmt.Errorf("create failed: %v", res.Err(cmd.UUID))
//	}
//
// # Batches are not transactional
//
// [Client.Sync] chunks commands at [MaxCommandsPerRequest] and merges the
// responses, but a successful call says nothing about the individual commands.
// Every uuid must be checked with [SyncResult.OK] or [SyncResult.Err]; a nil
// error with failed commands inside is the normal shape of a partial failure.
//
// # Priority is inverted
//
// Todoist's numeric priority runs backwards from its user-facing names: 4 is
// the UI's p1 (urgent) and 1 is the UI's p4 (normal). Todoist's own request
// documentation states this the wrong way round. See [PriorityFor].
//
// # Testing
//
// [New] takes a base URL, so a caller can point the client at an httptest
// server. [WithSleep] replaces the retry backoff so tests do not wait.
package todoist

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is the only supported Todoist API root.
const DefaultBaseURL = "https://api.todoist.com/api/v1"

// MaxPageLimit is the largest page size the list endpoints accept.
const MaxPageLimit = 200

// Client talks to the Todoist API v1.
type Client struct {
	baseURL string
	token   string
	http    *http.Client

	// sleep, when set, replaces the real wait. Tests use it so retry backoff
	// does not cost real time. Nil in production, where the wait is a
	// context-aware timer instead.
	sleep func(time.Duration)
	// maxRetryWait caps how long a 429 retry_after can park the process.
	maxRetryWait time.Duration
}

// Option customises a Client.
type Option func(*Client)

// WithHTTPClient sets the underlying HTTP client.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.http = h }
}

// WithSleep replaces the retry sleep function. Tests use it to avoid waiting.
func WithSleep(f func(time.Duration)) Option {
	return func(c *Client) { c.sleep = f }
}

// New returns a Client for baseURL. Pass DefaultBaseURL in production; tests
// pass an httptest server URL.
func New(baseURL, token string, opts ...Option) *Client {
	c := &Client{
		baseURL:      strings.TrimRight(baseURL, "/"),
		token:        token,
		http:         &http.Client{Timeout: 30 * time.Second},
		maxRetryWait: 60 * time.Second,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// APIError is a non-2xx response from Todoist.
type APIError struct {
	StatusCode int
	Body       string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	return fmt.Sprintf("todoist api: http %d: %s", e.StatusCode, truncate(e.Body, 300))
}

// retryable reports whether another attempt could plausibly succeed.
func (e *APIError) retryable() bool {
	return e.StatusCode == http.StatusTooManyRequests || e.StatusCode >= 500
}

// do performs a request, retrying once on 429 or 5xx. body is re-sent from a
// buffer so the retry does not need the caller to rebuild it.
//
// Retrying a POST is safe here only because every /sync command carries a
// deterministic uuid that Todoist treats as an idempotency key: a command the
// first attempt actually executed is a no-op on the second.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body []byte, contentType string, out any) error {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			wait := time.Second
			var apiErr *APIError
			if errors.As(lastErr, &apiErr) && apiErr.RetryAfter > 0 {
				wait = apiErr.RetryAfter
			}
			if wait > c.maxRetryWait {
				wait = c.maxRetryWait
			}
			if err := c.wait(ctx, wait); err != nil {
				return err
			}
		}

		err := c.doOnce(ctx, method, path, query, body, contentType, out)
		if err == nil {
			return nil
		}
		var apiErr *APIError
		if !errors.As(err, &apiErr) || !apiErr.retryable() {
			return err
		}
		lastErr = err
	}
	return lastErr
}

// wait pauses before a retry. A rate-limit retry_after can be up to a minute,
// so the wait honours context cancellation: Ctrl-C must not be inert just
// because the process is parked between attempts.
func (c *Client) wait(ctx context.Context, d time.Duration) error {
	if c.sleep != nil {
		c.sleep(d)
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *Client) doOnce(ctx context.Context, method, path string, query url.Values, body []byte, contentType string, out any) error {
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return fmt.Errorf("build request %s %s: %w", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response %s %s: %w", method, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{
			StatusCode: resp.StatusCode,
			Body:       string(respBody),
			RetryAfter: parseRetryAfter(resp, respBody),
		}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("decode response %s %s: %w", method, path, err)
	}
	return nil
}

// parseRetryAfter pulls a retry delay out of the error body's
// error_extra.retry_after (seconds), falling back to the Retry-After header.
func parseRetryAfter(resp *http.Response, body []byte) time.Duration {
	var payload struct {
		ErrorExtra struct {
			RetryAfter float64 `json:"retry_after"`
		} `json:"error_extra"`
	}
	if err := json.Unmarshal(body, &payload); err == nil && payload.ErrorExtra.RetryAfter > 0 {
		return time.Duration(payload.ErrorExtra.RetryAfter * float64(time.Second))
	}
	if h := resp.Header.Get("Retry-After"); h != "" {
		if secs, err := strconv.Atoi(h); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return 0
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
