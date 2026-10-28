// Package client is the only code that talks HTTP to MTN Cloud. It knows nothing about Terraform:
// it offers typed methods, typed errors, retries, rate limiting and a lookup cache. A Client is
// safe for concurrent use (terraform apply -parallelism=50) and holds no package-level state.
package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Logger receives one debug line per HTTP attempt. Fields never contain credentials: the client
// logs method, path, status, attempt and duration only. The provider wires this to tflog.
type Logger func(ctx context.Context, msg string, fields map[string]any)

// Config holds everything needed to build a Client.
type Config struct {
	URL string // console URL, for example https://console.cloud.mtn.ng

	// Either Token, or Username and Password (plus Tenant, the subdomain, for sub-tenant users).
	Token    string
	Username string
	Password string
	Tenant   string

	// Defaults from the provider block or MTNCLOUD_GROUP, MTNCLOUD_CLOUD, MTNCLOUD_PROJECT: the group
	// and cloud picked when the account has several, and the project used when a resource names none.
	Defaults Defaults

	Insecure  bool   // skip TLS verification
	UserAgent string // for example terraform-provider-mtncloud/1.2.3

	// Tuning. Zero values get the defaults below.
	MaxAttempts    int           // per request, including the first; default 5
	BackoffBase    time.Duration // default 500ms
	BackoffMax     time.Duration // default 15s
	RequestsPerSec float64       // client-side rate limit; default 10
	Burst          int           // default 20
	HTTPClient     *http.Client  // default: 60s timeout
	Logger         Logger

	// Waiting for asynchronous work (routers, instances, catalog orders): the first wait and the cap.
	PollMin time.Duration // default 500ms
	PollMax time.Duration // default 15s
}

// Defaults are the provider-wide group, cloud and project, by name. Empty means not set.
type Defaults struct {
	Group, Cloud, Project string
}

// Client is an authenticated MTN Cloud API client.
type Client struct {
	cfg     Config
	base    *url.URL
	http    *http.Client
	limiter *rate.Limiter
	log     Logger

	authMu sync.Mutex
	token  string

	cache lookupCache
}

// Defaults returns the provider-wide group, cloud and project.
func (c *Client) Defaults() Defaults { return c.cfg.Defaults }

// New validates cfg and returns a Client. It does not call the API: login happens on first use.
func New(cfg Config) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(cfg.URL, "/"))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("invalid MTN Cloud URL %q: use the full console address, for example https://console.cloud.mtn.ng", cfg.URL)
	}
	if cfg.Token == "" && (cfg.Username == "" || cfg.Password == "") {
		return nil, errors.New("no MTN Cloud credentials: set access_token (MTNCLOUD_API_TOKEN), or username and password (MTNCLOUD_API_USERNAME, MTNCLOUD_API_PASSWORD)")
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 5
	}
	if cfg.BackoffBase <= 0 {
		cfg.BackoffBase = 500 * time.Millisecond
	}
	if cfg.BackoffMax <= 0 {
		cfg.BackoffMax = 15 * time.Second
	}
	if cfg.RequestsPerSec <= 0 {
		cfg.RequestsPerSec = 10
	}
	if cfg.Burst <= 0 {
		cfg.Burst = 20
	}
	pollDefaults(&cfg)
	hc := cfg.HTTPClient
	if hc == nil {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		if cfg.Insecure {
			tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // opt-in via provider "secure = false"
		}
		hc = &http.Client{Timeout: 60 * time.Second, Transport: tr}
	}
	log := cfg.Logger
	if log == nil {
		log = func(context.Context, string, map[string]any) {}
	}
	return &Client{
		cfg:     cfg,
		base:    u,
		http:    hc,
		limiter: rate.NewLimiter(rate.Limit(cfg.RequestsPerSec), cfg.Burst),
		log:     log,
		token:   cfg.Token,
	}, nil
}

// get decodes a GET response into out. query may be nil.
func (c *Client) get(ctx context.Context, path string, query url.Values, out any) error {
	return c.do(ctx, http.MethodGet, path, query, nil, out)
}

// do sends one API request with auth, rate limiting and retries, and decodes the JSON answer.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return fmt.Errorf("encoding %s %s request: %w", method, path, err)
		}
	}
	token, err := c.accessToken(ctx)
	if err != nil {
		return err
	}
	resp, err := c.send(ctx, method, path, query, payload, "application/json", token)
	if err != nil {
		var apiErr *APIError
		// A token can expire mid-run. With a password we can log in again, once.
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized && c.cfg.Token == "" {
			if token, err = c.relogin(ctx, token); err != nil {
				return err
			}
			resp, err = c.send(ctx, method, path, query, payload, "application/json", token)
		}
		if err != nil {
			return err
		}
	}
	// Some refusals (an invalid or overlapping network, for example) can come back as a 2xx with
	// {"success": false, "msg": "..."}. They were not applied, so they are errors like any 4xx.
	var outcome struct {
		Success *bool `json:"success"`
	}
	if json.Unmarshal(resp, &outcome) == nil && outcome.Success != nil && !*outcome.Success {
		return newAPIError(method, path, http.StatusOK, resp)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(resp, out); err != nil {
		return fmt.Errorf("MTN Cloud API %s %s: unexpected response (%w)", method, path, err)
	}
	return nil
}

// send performs the HTTP exchange with retries and returns the body of a 2xx response.
func (c *Client) send(ctx context.Context, method, path string, query url.Values, payload []byte, contentType, token string) ([]byte, error) {
	idempotent := method == http.MethodGet || method == http.MethodHead
	u := *c.base
	u.Path = strings.TrimRight(u.Path, "/") + path
	if strings.Contains(path, "%") {
		// Already escaped by the caller (cypherPath): send it as it is, not escaped a second time.
		if p, err := url.PathUnescape(path); err == nil {
			u.Path = strings.TrimRight(c.base.Path, "/") + p
			u.RawPath = strings.TrimRight(c.base.EscapedPath(), "/") + path
		}
	}
	u.RawQuery = query.Encode()

	for attempt := 1; ; attempt++ {
		if err := c.limiter.Wait(ctx); err != nil {
			// The limiter refuses a wait that would end after ctx's deadline, before ctx itself has
			// ended: that is the deadline, and callers (timeouts {}) must see it as such.
			if _, ok := ctx.Deadline(); ok && ctx.Err() == nil {
				return nil, fmt.Errorf("%w (rate limit queue: %w)", context.DeadlineExceeded, err)
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		if payload != nil {
			req.Header.Set("Content-Type", contentType)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if c.cfg.UserAgent != "" {
			req.Header.Set("User-Agent", c.cfg.UserAgent)
		}

		start := time.Now()
		resp, err := c.http.Do(req)
		fields := map[string]any{"method": method, "path": path, "attempt": attempt, "duration_ms": time.Since(start).Milliseconds()}
		if err != nil {
			c.log(ctx, "MTN Cloud API request failed", fields)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if !idempotent || !isTransient(err) || attempt >= c.cfg.MaxAttempts {
				// *url.Error prints the full URL, query string included: report the cause only.
				var ue *url.Error
				if errors.As(err, &ue) {
					err = ue.Err
				}
				return nil, fmt.Errorf("MTN Cloud API %s %s: %w", method, path, err)
			}
			if err := sleep(ctx, backoff(attempt, c.cfg.BackoffBase, c.cfg.BackoffMax, "")); err != nil {
				return nil, err
			}
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		_ = resp.Body.Close()
		fields["status"] = resp.StatusCode
		c.log(ctx, "MTN Cloud API response", fields)

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if readErr != nil {
				return nil, fmt.Errorf("MTN Cloud API %s %s: reading response: %w", method, path, readErr)
			}
			return data, nil
		}
		if retryable(resp.StatusCode, idempotent) && attempt < c.cfg.MaxAttempts {
			if err := sleep(ctx, backoff(attempt, c.cfg.BackoffBase, c.cfg.BackoffMax, resp.Header.Get("Retry-After"))); err != nil {
				return nil, err
			}
			continue
		}
		return nil, newAPIError(method, path, resp.StatusCode, data)
	}
}

func isTransient(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) ||
		strings.Contains(err.Error(), "connection reset")
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// accessToken returns the bearer token, logging in with the password first if needed.
func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	if c.token != "" {
		return c.token, nil
	}
	return c.loginLocked(ctx)
}

// relogin replaces a token the API rejected, unless another goroutine already did.
func (c *Client) relogin(ctx context.Context, rejected string) (string, error) {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	if c.token != rejected {
		return c.token, nil
	}
	return c.loginLocked(ctx)
}

// loginLocked runs the OAuth password grant. Sub-tenant users log in as subdomain\username.
func (c *Client) loginLocked(ctx context.Context) (string, error) {
	username := c.cfg.Username
	if c.cfg.Tenant != "" {
		username = c.cfg.Tenant + `\` + c.cfg.Username
	}
	form := url.Values{
		"grant_type": {"password"},
		"scope":      {"write"},
		"client_id":  {"morph-api"}, // mtncloud:allow-upstream-name (OAuth client ID the API requires)
		"username":   {username},
		"password":   {c.cfg.Password},
	}
	data, err := c.send(ctx, http.MethodPost, "/oauth/token", nil, []byte(form.Encode()), "application/x-www-form-urlencoded", "")
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && (apiErr.StatusCode == http.StatusBadRequest || apiErr.StatusCode == http.StatusUnauthorized) {
			return "", fmt.Errorf("MTN Cloud login failed for %q: wrong username, password or tenant subdomain (%s)", username, apiErr.Message)
		}
		return "", err
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(data, &tok); err != nil || tok.AccessToken == "" {
		return "", errors.New("MTN Cloud login returned no access token")
	}
	c.token = tok.AccessToken
	return c.token, nil
}
