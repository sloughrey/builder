package wikiBot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

const userAgent = "Runik-builder/spoiler-cutoff (https://github.com/Runik-3/builder)"

// ClientOptions configures a polite, rate-limited MediaWiki API client.
type ClientOptions struct {
	// Requests per second across all workers. Defaults to 2.
	Rate float64
	// Directory for caching API responses on disk. Empty disables caching.
	CacheDir string
	// Contact info appended to the User-Agent, as wiki operators request.
	Contact string
	// Number of retries on throttling, server errors and network errors.
	Retries int
	// Per-request timeout. Defaults to 30s.
	Timeout time.Duration
	// Used in tests to skip backoff sleeps.
	BackoffBase time.Duration
}

// Client fetches from one MediaWiki api.php endpoint.
type Client struct {
	apiUrl string
	opts   ClientOptions
	http   *http.Client

	mu   sync.Mutex
	next time.Time
}

func NewClient(apiUrl string, opts ClientOptions) *Client {
	if opts.Rate <= 0 {
		opts.Rate = 2
	}
	if opts.Retries <= 0 {
		opts.Retries = 5
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.BackoffBase <= 0 {
		opts.BackoffBase = 2 * time.Second
	}
	return &Client{apiUrl: apiUrl, opts: opts, http: &http.Client{Timeout: opts.Timeout}}
}

type apiError struct {
	Error *struct {
		Code string `json:"code"`
		Info string `json:"info"`
	} `json:"error"`
}

// wait blocks until the rate limiter allows another request.
func (c *Client) wait() {
	interval := time.Duration(float64(time.Second) / c.opts.Rate)
	c.mu.Lock()
	now := time.Now()
	if c.next.Before(now) {
		c.next = now
	}
	sleep := c.next.Sub(now)
	c.next = c.next.Add(interval)
	c.mu.Unlock()
	time.Sleep(sleep)
}

func (c *Client) cachePath(query string) string {
	sum := sha256.Sum256([]byte(c.apiUrl + "?" + query))
	h := hex.EncodeToString(sum[:])
	return filepath.Join(c.opts.CacheDir, h[:2], h+".json")
}

// Get performs an API request with the given parameters and decodes the JSON
// response into out. Cached responses are reused when caching is enabled.
func (c *Client) Get(params url.Values, out any) error {
	params.Set("format", "json")
	params.Set("formatversion", "2")
	params.Set("maxlag", "5")
	query := params.Encode()

	if c.opts.CacheDir != "" {
		if data, err := os.ReadFile(c.cachePath(query)); err == nil {
			if json.Unmarshal(data, out) == nil {
				return nil
			}
		}
	}

	var lastErr error
	for attempt := 0; attempt <= c.opts.Retries; attempt++ {
		if attempt > 0 {
			time.Sleep(c.backoff(attempt, lastErr))
		}
		data, err := c.do(query)
		if err != nil {
			lastErr = err
			var r retryable
			if errors.As(err, &r) {
				continue
			}
			return err
		}
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("could not decode wiki response: %w", err)
		}
		if c.opts.CacheDir != "" {
			path := c.cachePath(query)
			if os.MkdirAll(filepath.Dir(path), 0o755) == nil {
				_ = os.WriteFile(path, data, 0o644)
			}
		}
		return nil
	}
	return fmt.Errorf("giving up after %d retries: %w", c.opts.Retries, lastErr)
}

type retryable struct {
	err        error
	retryAfter time.Duration
}

func (r retryable) Error() string { return r.err.Error() }

func (c *Client) backoff(attempt int, err error) time.Duration {
	var r retryable
	if errors.As(err, &r) && r.retryAfter > 0 {
		return r.retryAfter
	}
	d := c.opts.BackoffBase << (attempt - 1)
	if d > time.Minute {
		d = time.Minute
	}
	return d
}

func (c *Client) do(query string) ([]byte, error) {
	c.wait()

	req, err := http.NewRequest("GET", c.apiUrl+"?"+query, nil)
	if err != nil {
		return nil, err
	}
	ua := userAgent
	if c.opts.Contact != "" {
		ua += " contact: " + c.opts.Contact
	}
	req.Header.Set("User-Agent", ua)

	res, err := c.http.Do(req)
	if err != nil {
		return nil, retryable{err: err}
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, retryable{err: err}
	}

	if res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500 {
		retryAfter := time.Duration(0)
		if s, err := strconv.Atoi(res.Header.Get("Retry-After")); err == nil {
			retryAfter = time.Duration(s) * time.Second
		}
		return nil, retryable{err: fmt.Errorf("wiki returned %s", res.Status), retryAfter: retryAfter}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("wiki returned %s", res.Status)
	}

	var apiErr apiError
	if json.Unmarshal(body, &apiErr) == nil && apiErr.Error != nil {
		if apiErr.Error.Code == "maxlag" || apiErr.Error.Code == "ratelimited" {
			return nil, retryable{err: fmt.Errorf("wiki asked us to slow down (%s)", apiErr.Error.Code), retryAfter: 5 * time.Second}
		}
		return nil, fmt.Errorf("wiki API error %s: %s", apiErr.Error.Code, apiErr.Error.Info)
	}
	return body, nil
}
