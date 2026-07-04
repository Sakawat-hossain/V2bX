// Package panel implements an HTTP client for the UniProxy-shaped node
// communication API exposed by V2board-family panels (XBoard, V2Board, and
// any compatible fork). Base URL, API key, and endpoint paths are all
// config-driven so the same client works against any panel implementing the
// same contract.
package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// Client talks to a single panel instance on behalf of one node.
type Client struct {
	baseURL    *url.URL
	apiKey     string
	configPath string
	userPath   string
	pushPath   string
	alivePath  string

	httpClient *http.Client
	logger     *slog.Logger

	// cacheMu guards the ETag cache for GET endpoints. The panel returns an
	// ETag on config/user and honors If-None-Match with a 304, so unchanged
	// syncs transfer an empty body — a big saving at scale. On 304 we serve
	// the last body back transparently, so callers never see the difference.
	cacheMu sync.Mutex
	cache   map[string]*cacheEntry
}

type cacheEntry struct {
	etag string
	body []byte
}

// Options configures a new Client. Paths default to the standard UniProxy
// routes when left empty.
type Options struct {
	ApiHost    string
	ApiKey     string
	ConfigPath string
	UserPath   string
	PushPath   string
	AlivePath  string
	HTTPClient *http.Client
	Logger     *slog.Logger
}

// New builds a Client from Options, validating the base URL.
func New(opts Options) (*Client, error) {
	u, err := url.Parse(opts.ApiHost)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("panel: invalid api_host %q", opts.ApiHost)
	}
	c := &Client{
		baseURL:    u,
		apiKey:     opts.ApiKey,
		configPath: firstNonEmpty(opts.ConfigPath, "/api/v1/server/UniProxy/config"),
		userPath:   firstNonEmpty(opts.UserPath, "/api/v1/server/UniProxy/user"),
		pushPath:   firstNonEmpty(opts.PushPath, "/api/v1/server/UniProxy/push"),
		alivePath:  firstNonEmpty(opts.AlivePath, "/api/v1/server/UniProxy/alive"),
		httpClient: opts.HTTPClient,
		logger:     opts.Logger,
		cache:      map[string]*cacheEntry{},
	}
	if c.httpClient == nil {
		c.httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	if c.logger == nil {
		c.logger = slog.Default()
	}
	return c, nil
}

func firstNonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func (c *Client) endpoint(path string, query url.Values) string {
	u := *c.baseURL
	u.Path = path
	if query != nil {
		query.Set("token", c.apiKey)
		u.RawQuery = query.Encode()
	} else {
		q := url.Values{"token": {c.apiKey}}
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// FetchNodeConfig retrieves node configuration for nodeID.
func (c *Client) FetchNodeConfig(ctx context.Context, nodeID int64, nodeType string) (*NodeConfigResponse, error) {
	q := url.Values{"node_id": {strconv.FormatInt(nodeID, 10)}, "node_type": {nodeType}}
	var resp NodeConfigResponse
	if err := c.getJSON(ctx, c.configPath, q, &resp); err != nil {
		return nil, fmt.Errorf("panel: fetch node config: %w", err)
	}
	return &resp, nil
}

// FetchUsers retrieves the current subscriber list for nodeID.
func (c *Client) FetchUsers(ctx context.Context, nodeID int64, nodeType string) ([]UserResponse, error) {
	q := url.Values{"node_id": {strconv.FormatInt(nodeID, 10)}, "node_type": {nodeType}}

	raw, err := c.get(ctx, c.userPath, q)
	if err != nil {
		return nil, fmt.Errorf("panel: fetch users: %w", err)
	}

	// Some panels return {"users":[...]}, others a bare array. Try both.
	var wrapped UserListResponse
	if err := json.Unmarshal(raw, &wrapped); err == nil && wrapped.Users != nil {
		return wrapped.Users, nil
	}
	var bare []UserResponse
	if err := json.Unmarshal(raw, &bare); err != nil {
		return nil, fmt.Errorf("panel: fetch users: unrecognized response shape: %w", err)
	}
	return bare, nil
}

// PushTraffic reports accumulated per-user traffic deltas for nodeID. The
// panel expects an object keyed by user id: {"1": [upload, download], ...}.
func (c *Client) PushTraffic(ctx context.Context, nodeID int64, nodeType string, records []TrafficRecord) error {
	body := make(map[string][2]uint64, len(records))
	for _, r := range records {
		body[strconv.FormatInt(r.UID, 10)] = [2]uint64{r.Upload, r.Download}
	}
	q := url.Values{"node_id": {strconv.FormatInt(nodeID, 10)}, "node_type": {nodeType}}
	if _, err := c.postJSON(ctx, c.pushPath, q, body); err != nil {
		return fmt.Errorf("panel: push traffic: %w", err)
	}
	return nil
}

// ReportAlive reports currently-online users for nodeID. The panel expects an
// object keyed by user id: {"1": ["1.2.3.4"], ...}.
func (c *Client) ReportAlive(ctx context.Context, nodeID int64, nodeType string, records []AliveRecord) error {
	body := make(map[string][]string, len(records))
	for _, r := range records {
		body[strconv.FormatInt(r.UID, 10)] = append(body[strconv.FormatInt(r.UID, 10)], r.IP)
	}
	q := url.Values{"node_id": {strconv.FormatInt(nodeID, 10)}, "node_type": {nodeType}}
	if _, err := c.postJSON(ctx, c.alivePath, q, body); err != nil {
		return fmt.Errorf("panel: report alive: %w", err)
	}
	return nil
}

// get performs a conditional GET: it sends If-None-Match with the last ETag
// seen for this endpoint and, on a 304, returns the cached body so callers
// never have to care whether the data changed. On a 200 it caches the new
// ETag and body.
func (c *Client) get(ctx context.Context, path string, query url.Values) ([]byte, error) {
	endpoint := c.endpoint(path, query)

	c.cacheMu.Lock()
	entry := c.cache[endpoint]
	c.cacheMu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if entry != nil && entry.etag != "" {
		req.Header.Set("If-None-Match", entry.etag)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		if entry != nil {
			return entry.body, nil
		}
		return nil, fmt.Errorf("panel: got 304 with no cached body")
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, truncate(body, 500))
	}

	if etag := resp.Header.Get("ETag"); etag != "" {
		c.cacheMu.Lock()
		c.cache[endpoint] = &cacheEntry{etag: etag, body: body}
		c.cacheMu.Unlock()
	}
	return body, nil
}

func (c *Client) getJSON(ctx context.Context, path string, query url.Values, out any) error {
	raw, err := c.get(ctx, path, query)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func (c *Client) postJSON(ctx context.Context, path string, query url.Values, body any) ([]byte, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(path, query), bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req)
}

func (c *Client) do(req *http.Request) ([]byte, error) {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, truncate(body, 500))
	}
	return body, nil
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}

// RetryConfig controls exponential backoff for SyncWithRetry.
type RetryConfig struct {
	InitialDelay time.Duration
	MaxDelay     time.Duration
	MaxAttempts  int // 0 = unlimited
}

// DefaultRetryConfig is a sensible backoff for panel sync loops: 1s, 2s, 4s,
// ... capped at 60s, retried indefinitely so a briefly-unreachable panel
// never brings the agent down.
var DefaultRetryConfig = RetryConfig{
	InitialDelay: 1 * time.Second,
	MaxDelay:     60 * time.Second,
	MaxAttempts:  0,
}

// WithRetry runs fn, retrying with exponential backoff on error until it
// succeeds, ctx is cancelled, or MaxAttempts is exhausted. Every failure is
// logged so operators can see the panel is unreachable without the agent
// crashing or dropping its last-known-good state.
func WithRetry(ctx context.Context, logger *slog.Logger, rc RetryConfig, name string, fn func(ctx context.Context) error) error {
	if logger == nil {
		logger = slog.Default()
	}
	delay := rc.InitialDelay
	if delay <= 0 {
		delay = time.Second
	}
	attempt := 0
	for {
		attempt++
		err := fn(ctx)
		if err == nil {
			return nil
		}
		logger.Warn("panel sync failed, retrying", "operation", name, "attempt", attempt, "delay", delay, "error", err)
		if rc.MaxAttempts > 0 && attempt >= rc.MaxAttempts {
			return fmt.Errorf("%s: giving up after %d attempts: %w", name, attempt, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay *= 2
		if rc.MaxDelay > 0 && delay > rc.MaxDelay {
			delay = rc.MaxDelay
		}
	}
}
