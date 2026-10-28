package advisory

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

const DefaultFeedURL = "https://raw.githubusercontent.com/hakopod/hakopod/security-advisories/security-advisories.signed.json"

type FetchResult struct {
	Body        []byte
	ETag        string
	NotModified bool
	RetryAfter  time.Duration
}

// Fetch downloads the signed feed. The body is not verified here.
func Fetch(ctx context.Context, client *http.Client, url, etag string) (FetchResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return FetchResult{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "hakopod-advisories")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := client.Do(req)
	if err != nil {
		return FetchResult{}, err
	}
	defer resp.Body.Close()
	res := FetchResult{ETag: resp.Header.Get("ETag")}
	if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && secs > 0 {
		res.RetryAfter = time.Duration(secs) * time.Second
	}
	if resp.StatusCode == http.StatusNotModified {
		res.NotModified = true
		if res.ETag == "" {
			res.ETag = etag
		}
		return res, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return res, fmt.Errorf("advisory feed: unexpected status %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFeedBytes+1))
	if err != nil {
		return res, err
	}
	if len(body) > maxFeedBytes {
		return res, fmt.Errorf("advisory feed exceeds %d bytes", maxFeedBytes)
	}
	res.Body = body
	return res, nil
}

// Backoff = max(retryAfter, min(6h, 60s*2^failures)).
func Backoff(failures int, retryAfter time.Duration) time.Duration {
	d := 6 * time.Hour
	if failures < 9 { // 60s*2^9 already exceeds 6h
		d = min(d, time.Minute<<max(failures, 0))
	}
	return max(d, retryAfter)
}
