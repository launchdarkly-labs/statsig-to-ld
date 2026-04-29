// Package httputil provides shared HTTP client utilities including
// retry logic with exponential backoff and context-aware cancellation.
package httputil

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"
)

const (
	maxRetries    = 3
	maxRetryAfter = 60 * time.Second
)

// version is the User-Agent version string. Set via SetVersion at startup.
var version = "dev"

// SetVersion sets the version string used in the User-Agent header.
func SetVersion(v string) {
	version = v
}

func userAgent() string {
	return "statsig-metric-importer/" + version
}

// DoWithRetry executes an HTTP request with retry for transient failures
// (429 rate-limit and 5xx server errors). Retries use exponential backoff
// with jitter. For 429 responses, the Retry-After header is respected
// (capped at 60 seconds, with jitter applied).
//
// The request body is reconstructed from reqBody on each retry attempt
// since the original reader is consumed. Pass nil for GET requests.
//
// Cancelling the context will abort both in-flight requests and backoff
// sleeps immediately.
func DoWithRetry(ctx context.Context, client *http.Client, req *http.Request, reqBody []byte) ([]byte, int, error) {
	// Set User-Agent if not already present
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", userAgent())
	}

	var lastErr error

	for attempt := range maxRetries {
		if attempt > 0 && reqBody != nil {
			req.Body = io.NopCloser(bytes.NewReader(reqBody))
		}

		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("network error: %w", err)
			if sleepErr := sleepWithContext(ctx, backoffDuration(attempt)); sleepErr != nil {
				return nil, 0, sleepErr
			}
			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("reading response body (HTTP %d): %w", resp.StatusCode, err)
			if sleepErr := sleepWithContext(ctx, backoffDuration(attempt)); sleepErr != nil {
				return nil, 0, sleepErr
			}
			continue
		}

		// 429 and 5xx are retryable; everything else (including 409) is returned immediately
		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, Truncate(string(body), 200))
			delay := backoffDuration(attempt)
			if resp.StatusCode == 429 {
				if ra := resp.Header.Get("Retry-After"); ra != "" {
					if secs, parseErr := strconv.Atoi(ra); parseErr == nil && secs > 0 {
						d := time.Duration(secs) * time.Second
						if d > maxRetryAfter {
							d = maxRetryAfter
						}
						// Add ±25% jitter to Retry-After to prevent thundering herd
						// when multiple concurrent workers receive the same value
						jitter := float64(d) * 0.25 * (2*rand.Float64() - 1)
						delay = d + time.Duration(jitter)
					}
				}
			}
			if sleepErr := sleepWithContext(ctx, delay); sleepErr != nil {
				return nil, 0, sleepErr
			}
			continue
		}

		return body, resp.StatusCode, nil
	}

	return nil, 0, fmt.Errorf("request failed after %d attempts: %w", maxRetries, lastErr)
}

// sleepWithContext sleeps for the given duration, returning immediately
// with ctx.Err() if the context is cancelled.
func sleepWithContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// backoffDuration returns an exponential backoff duration with ±25% jitter.
// attempt 0 → ~1s, attempt 1 → ~2s, attempt 2 → ~4s.
func backoffDuration(attempt int) time.Duration {
	base := math.Pow(2, float64(attempt))
	jitter := base * 0.25 * (2*rand.Float64() - 1) // ±25%
	return time.Duration((base + jitter) * float64(time.Second))
}

// Truncate returns s truncated to max runes with "..." appended if truncated.
// Safe for multi-byte UTF-8 strings.
func Truncate(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max]) + "..."
}
