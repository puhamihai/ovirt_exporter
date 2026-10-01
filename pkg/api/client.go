// SPDX-License-Identifier: MIT

// Package api is the oVirt REST API client used by the exporter.
//
// It replaces github.com/czerwonk/ovirt_api, whose re-authentication let every
// goroutine that received a 401 log in on its own. With a per-VM fan-out that
// turned one expired session into ~130 parallel HTTP Basic logins, which
// exhausted the engine's request threads (each Basic login makes the engine call
// its own SSO service back through the same thread pool) and deadlocked it.
//
// This client therefore:
//   - authenticates once and reuses the credential: an OAuth bearer token by
//     default, or a persistent-auth JSESSIONID session as a fallback;
//   - re-authenticates single-flight: when many requests see a 401 at once,
//     exactly one login is sent and every other request waits for its result,
//     then retries once;
//   - backs off exponentially after a failed login, failing requests fast
//     instead of hitting the engine while the backoff runs;
//   - bounds the number of requests in flight to the engine with a semaphore.
package api

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// AuthMethod selects how the client authenticates against the engine.
type AuthMethod string

const (
	// AuthOAuth obtains an SSO access token (POST /ovirt-engine/sso/oauth/token,
	// grant_type=password, scope=ovirt-app-api) and sends it as a bearer token.
	AuthOAuth AuthMethod = "oauth"
	// AuthSession logs in once with HTTP Basic and "Prefer: persistent-auth",
	// then sends the JSESSIONID cookie on every request.
	AuthSession AuthMethod = "session"
)

const (
	// DefaultMaxConcurrentRequests is the default number of API requests the
	// client allows in flight at the same time.
	DefaultMaxConcurrentRequests = 8

	oauthScope        = "ovirt-app-api"
	defaultLoginTO    = 30 * time.Second
	defaultMinBackoff = 5 * time.Second
	defaultMaxBackoff = 5 * time.Minute
	// a token that expires within this window is renewed before it is used
	tokenRefreshMargin = time.Minute
)

// ErrUnauthorized is returned when the engine still answers 401 after the
// client re-authenticated.
var ErrUnauthorized = errors.New("401 Unauthorized")

// Client encapsulates communication with the oVirt REST API. It is safe for
// concurrent use and is meant to be shared by every collector.
type Client struct {
	url       string
	ssoURL    string
	revokeURL string
	username  string
	password  string

	authMethod   AuthMethod
	insecure     bool
	maxRequests  int
	loginTimeout time.Duration
	minBackoff   time.Duration
	maxBackoff   time.Duration
	logger       Logger
	debug        bool
	httpClient   *http.Client
	now          func() time.Time

	// sem bounds the number of requests in flight to the API
	sem chan struct{}

	mu sync.Mutex
	// credential is the bearer token (oauth) or the session cookie (session);
	// empty when no valid credential is held
	credential string
	expiry     time.Time
	// generation is incremented on every successful login, so a request that
	// got a 401 can tell whether somebody else already logged in again
	generation   uint64
	inflight     *loginCall
	failures     int
	lastLoginErr error
	nextLoginAt  time.Time
}

type loginCall struct {
	done chan struct{}
	err  error
}

// ClientOption applies options to Client
type ClientOption func(*Client)

// WithInsecure disables TLS certificate validation
func WithInsecure() ClientOption {
	return func(c *Client) {
		c.insecure = true
	}
}

// WithLogger sets the logger for the API client
func WithLogger(l Logger) ClientOption {
	return func(c *Client) {
		c.logger = l
	}
}

// WithDebug enables debug mode
func WithDebug() ClientOption {
	return func(c *Client) {
		c.debug = true
	}
}

// WithAuthMethod selects the authentication method (default: AuthOAuth)
func WithAuthMethod(m AuthMethod) ClientOption {
	return func(c *Client) {
		c.authMethod = m
	}
}

// WithMaxConcurrentRequests bounds the number of API requests in flight
// (default: DefaultMaxConcurrentRequests)
func WithMaxConcurrentRequests(n int) ClientOption {
	return func(c *Client) {
		c.maxRequests = n
	}
}

// WithLoginBackoff sets the delay before a new login is attempted after a
// failed one; it doubles on every consecutive failure up to max
func WithLoginBackoff(min, max time.Duration) ClientOption {
	return func(c *Client) {
		c.minBackoff = min
		c.maxBackoff = max
	}
}

// WithHTTPClient replaces the HTTP client (used by tests)
func WithHTTPClient(hc *http.Client) ClientOption {
	return func(c *Client) {
		c.httpClient = hc
	}
}

// withClock replaces the clock (used by tests)
func withClock(now func() time.Time) ClientOption {
	return func(c *Client) {
		c.now = now
	}
}

// NewClient returns a new client and performs the initial login, so wrong
// credentials or an unreachable engine are reported at startup.
func NewClient(apiURL, username, password string, opts ...ClientOption) (*Client, error) {
	c, err := newClient(apiURL, username, password, opts...)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), c.loginTimeout)
	defer cancel()

	if _, _, err := c.reauthenticate(ctx, 0); err != nil {
		return nil, err
	}

	return c, nil
}

func newClient(apiURL, username, password string, opts ...ClientOption) (*Client, error) {
	c := &Client{
		url:          apiURL,
		username:     username,
		password:     password,
		authMethod:   AuthOAuth,
		maxRequests:  DefaultMaxConcurrentRequests,
		loginTimeout: defaultLoginTO,
		minBackoff:   defaultMinBackoff,
		maxBackoff:   defaultMaxBackoff,
		logger:       &defaultLogger{},
		now:          time.Now,
	}

	for _, o := range opts {
		o(c)
	}

	if c.authMethod != AuthOAuth && c.authMethod != AuthSession {
		return nil, fmt.Errorf("unsupported auth method %q (want %q or %q)", c.authMethod, AuthOAuth, AuthSession)
	}

	if c.maxRequests < 1 {
		return nil, fmt.Errorf("max concurrent requests must be at least 1, got %d", c.maxRequests)
	}
	c.sem = make(chan struct{}, c.maxRequests)

	u, err := url.Parse(apiURL)
	if err != nil {
		return nil, fmt.Errorf("invalid API URL %q: %w", apiURL, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("invalid API URL %q: scheme and host are required", apiURL)
	}
	// same derivation as the oVirt Python SDK: the SSO service lives next to
	// the API on the engine, independent of the API path
	base := u.Scheme + "://" + u.Host
	c.ssoURL = base + "/ovirt-engine/sso/oauth/token"
	c.revokeURL = base + "/ovirt-engine/services/sso-logout"

	if c.httpClient == nil {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.MaxIdleConnsPerHost = c.maxRequests
		if c.insecure {
			tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		}
		c.httpClient = &http.Client{Transport: tr}
	}

	return c, nil
}

// GetAndParse retrieves XML data from the API and unmarshals it
func (c *Client) GetAndParse(ctx context.Context, path string, v interface{}) error {
	b, err := c.Get(ctx, path)
	if err != nil {
		return err
	}

	return xml.Unmarshal(b, v)
}

// Get retrieves XML data from the API and returns it
func (c *Client) Get(ctx context.Context, path string) ([]byte, error) {
	return c.SendRequest(ctx, path, http.MethodGet, nil)
}

// SendRequest sends a request to the API. On a 401 the client re-authenticates
// (single-flight, shared with every concurrent request) and retries once.
func (c *Client) SendRequest(ctx context.Context, path, method string, body []byte) ([]byte, error) {
	uri := strings.TrimRight(c.url, "/") + "/" + strings.Trim(path, "/")

	status, b, gen, err := c.send(ctx, method, uri, body)
	if err != nil {
		return nil, err
	}

	if status == http.StatusUnauthorized {
		c.logger.Debugf("%s %s: 401, re-authenticating", method, uri)

		if _, _, err := c.reauthenticate(ctx, gen); err != nil {
			return nil, fmt.Errorf("%s %s: %w (re-authentication failed: %v)", method, uri, ErrUnauthorized, err)
		}

		status, b, _, err = c.send(ctx, method, uri, body)
		if err != nil {
			return nil, err
		}
		if status == http.StatusUnauthorized {
			return nil, fmt.Errorf("%s %s: %w after re-authentication", method, uri, ErrUnauthorized)
		}
	}

	if status >= 300 {
		return nil, fmt.Errorf("%s %s: %d %s", method, uri, status, http.StatusText(status))
	}

	return b, nil
}

// send performs one HTTP request while holding a concurrency slot. The
// credential is picked up only once the slot is held, so requests queued behind
// a re-login go out with the new credential instead of collecting more 401s.
// It returns the credential generation that was used.
func (c *Client) send(ctx context.Context, method, uri string, body []byte) (int, []byte, uint64, error) {
	select {
	case c.sem <- struct{}{}:
	case <-ctx.Done():
		return 0, nil, 0, ctx.Err()
	}
	defer func() { <-c.sem }()

	cred, gen, err := c.currentCredential(ctx)
	if err != nil {
		return 0, nil, 0, fmt.Errorf("%s %s: not authenticated: %w", method, uri, err)
	}

	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, uri, r)
	if err != nil {
		return 0, nil, 0, err
	}

	req.Header.Set("Accept", "application/xml")
	if body != nil {
		req.Header.Set("Content-Type", "application/xml")
	}
	c.setCredential(req, cred)

	c.logger.Debugf("%s %s", method, uri)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, nil, 0, err
	}
	defer resp.Body.Close()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, 0, err
	}

	c.logger.Debugf("Status Code: %s", resp.Status)
	if c.debug {
		c.logger.Debugf("Response: %s", string(b))
	}

	return resp.StatusCode, b, gen, nil
}

func (c *Client) setCredential(req *http.Request, cred string) {
	switch c.authMethod {
	case AuthOAuth:
		req.Header.Set("Authorization", "Bearer "+cred)
	case AuthSession:
		req.Header.Set("Prefer", "persistent-auth")
		req.Header.Set("Cookie", cred)
	}
}

// currentCredential returns the credential to use for a request. It only logs
// in (single-flight) when there is no credential, a login is already running,
// or the token is about to expire.
func (c *Client) currentCredential(ctx context.Context) (string, uint64, error) {
	c.mu.Lock()
	cred, gen := c.credential, c.generation
	needLogin := cred == "" || c.inflight != nil || (c.expiresSoon() && !c.inBackoff())
	c.mu.Unlock()

	if !needLogin {
		return cred, gen, nil
	}

	return c.reauthenticate(ctx, gen)
}

// reauthenticate makes sure exactly one login runs for the credential
// generation staleGen. Callers that arrive while a login is running wait for
// it; callers that arrive after it finished get its result without logging in
// again. During the backoff after a failed login it fails fast.
func (c *Client) reauthenticate(ctx context.Context, staleGen uint64) (string, uint64, error) {
	c.mu.Lock()

	if c.inflight == nil && c.generation != staleGen && c.credential != "" {
		// somebody else already logged in after our request was sent
		cred, gen := c.credential, c.generation
		c.mu.Unlock()
		return cred, gen, nil
	}

	call := c.inflight
	if call == nil {
		if c.inBackoff() {
			err := fmt.Errorf("login suppressed for %s after %d failed attempt(s): %w",
				c.nextLoginAt.Sub(c.now()).Round(time.Second), c.failures, c.lastLoginErr)
			c.mu.Unlock()
			return "", 0, err
		}

		// the credential is known to be bad (or about to be); stop handing it
		// out so requests wait for the login instead of collecting more 401s
		c.credential = ""
		call = &loginCall{done: make(chan struct{})}
		c.inflight = call
		go c.runLogin(call)
	}
	c.mu.Unlock()

	select {
	case <-call.done:
	case <-ctx.Done():
		return "", 0, ctx.Err()
	}

	if call.err != nil {
		return "", 0, call.err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	return c.credential, c.generation, nil
}

// runLogin performs the login for a loginCall. It does not use any caller's
// context: one scrape being cancelled must not fail the login for everybody.
func (c *Client) runLogin(call *loginCall) {
	ctx, cancel := context.WithTimeout(context.Background(), c.loginTimeout)
	defer cancel()

	cred, expiry, err := c.login(ctx)

	c.mu.Lock()
	if err != nil {
		c.failures++
		c.lastLoginErr = err
		backoff := c.backoff()
		c.nextLoginAt = c.now().Add(backoff)
		c.logger.Errorf("oVirt API login as %s failed (attempt %d), next attempt in %s: %v", c.username, c.failures, backoff, err)
	} else {
		if c.generation > 0 {
			c.logger.Infof("re-authenticated to oVirt API as %s", c.username)
		}
		c.credential = cred
		c.expiry = expiry
		c.generation++
		c.failures = 0
		c.lastLoginErr = nil
		c.nextLoginAt = time.Time{}
	}
	call.err = err
	c.inflight = nil
	c.mu.Unlock()

	close(call.done)
}

func (c *Client) backoff() time.Duration {
	d := c.minBackoff
	for i := 1; i < c.failures && d < c.maxBackoff; i++ {
		d *= 2
	}
	if d > c.maxBackoff {
		d = c.maxBackoff
	}
	return d
}

// inBackoff must be called with c.mu held
func (c *Client) inBackoff() bool {
	return !c.nextLoginAt.IsZero() && c.now().Before(c.nextLoginAt)
}

// expiresSoon must be called with c.mu held
func (c *Client) expiresSoon() bool {
	return !c.expiry.IsZero() && c.now().Add(tokenRefreshMargin).After(c.expiry)
}

func (c *Client) login(ctx context.Context) (string, time.Time, error) {
	if c.authMethod == AuthSession {
		cookie, err := c.loginSession(ctx)
		return cookie, time.Time{}, err
	}

	return c.loginOAuth(ctx)
}

type ssoResponse struct {
	AccessToken      string      `json:"access_token"`
	Exp              interface{} `json:"exp"`
	Error            string      `json:"error"`
	ErrorCode        string      `json:"error_code"`
	ErrorDescription string      `json:"error_description"`
}

func (c *Client) loginOAuth(ctx context.Context) (string, time.Time, error) {
	form := url.Values{
		"grant_type": {"password"},
		"scope":      {oauthScope},
		"username":   {c.username},
		"password":   {c.password},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.ssoURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	c.logger.Debugf("POST %s", c.ssoURL)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", time.Time{}, err
	}
	defer resp.Body.Close()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", time.Time{}, err
	}

	var sso ssoResponse
	if err := json.Unmarshal(b, &sso); err != nil {
		return "", time.Time{}, fmt.Errorf("SSO token request: %s: could not parse response: %w", resp.Status, err)
	}

	if sso.AccessToken == "" {
		msg := sso.ErrorDescription
		if msg == "" {
			msg = sso.Error
		}
		return "", time.Time{}, fmt.Errorf("SSO token request: %s: %s %s", resp.Status, sso.ErrorCode, msg)
	}

	return sso.AccessToken, parseExpiry(sso.Exp), nil
}

// parseExpiry interprets the "exp" field of the SSO response, which the engine
// sends as epoch milliseconds (as a string). Unknown formats yield the zero
// time, which disables proactive renewal: the token is then renewed on 401.
func parseExpiry(v interface{}) time.Time {
	var n float64
	switch e := v.(type) {
	case string:
		f, err := strconv.ParseFloat(e, 64)
		if err != nil {
			return time.Time{}
		}
		n = f
	case float64:
		n = e
	default:
		return time.Time{}
	}

	if n <= 0 {
		return time.Time{}
	}
	if n < 1e12 { // seconds rather than milliseconds
		n *= 1000
	}

	return time.UnixMilli(int64(n))
}

func (c *Client) loginSession(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, c.url, nil)
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(c.username, c.password)
	req.Header.Set("Prefer", "persistent-auth")

	c.logger.Debugf("HEAD %s", c.url)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("session login: %s", resp.Status)
	}

	for _, ck := range resp.Cookies() {
		if ck.Name == "JSESSIONID" {
			return ck.Name + "=" + ck.Value, nil
		}
	}

	return "", errors.New("session login: engine did not return a JSESSIONID cookie")
}

// Close terminates the SSO session with the API
func (c *Client) Close() {
	c.mu.Lock()
	cred := c.credential
	c.credential = ""
	c.mu.Unlock()

	if cred == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), c.loginTimeout)
	defer cancel()

	var req *http.Request
	var err error
	switch c.authMethod {
	case AuthOAuth:
		form := url.Values{"scope": {oauthScope}, "token": {cred}}
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, c.revokeURL, strings.NewReader(form.Encode()))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Accept", "application/json")
		}
	case AuthSession:
		// a request without "Prefer: persistent-auth" closes the session
		req, err = http.NewRequestWithContext(ctx, http.MethodHead, c.url, nil)
		if err == nil {
			req.Header.Set("Cookie", cred)
		}
	}
	if err != nil {
		return
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.logger.Errorf("could not close oVirt API session: %v", err)
		return
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}
