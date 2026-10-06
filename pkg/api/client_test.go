// SPDX-License-Identifier: MIT

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeEngine imitates the parts of the engine the client talks to: the SSO
// token endpoint, the persistent-auth HEAD login and the API itself.
type fakeEngine struct {
	t      *testing.T
	server *httptest.Server

	mu    sync.Mutex
	valid string // the one credential the API currently accepts
	seq   int

	logins     atomic.Int32 // login attempts (token POSTs or Basic HEADs)
	apiHits    atomic.Int32
	inFlight   atomic.Int32
	maxFlight  atomic.Int32
	failLogins atomic.Bool

	// optional hooks
	loginDelay time.Duration
	apiDelay   time.Duration
	onUnauth   func() // called before a 401 is written
}

func newFakeEngine(t *testing.T) *fakeEngine {
	e := &fakeEngine{t: t}
	mux := http.NewServeMux()
	mux.HandleFunc("/ovirt-engine/sso/oauth/token", e.handleToken)
	mux.HandleFunc("/ovirt-engine/api/", e.handleAPI)
	e.server = httptest.NewServer(mux)
	t.Cleanup(e.server.Close)
	return e
}

func (e *fakeEngine) apiURL() string { return e.server.URL + "/ovirt-engine/api/" }

// expire invalidates the current credential, like the engine dropping a session
func (e *fakeEngine) expire() {
	e.mu.Lock()
	e.valid = "expired-" + e.valid
	e.mu.Unlock()
}

func (e *fakeEngine) issue() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.seq++
	e.valid = fmt.Sprintf("cred-%d", e.seq)
	return e.valid
}

func (e *fakeEngine) handleToken(w http.ResponseWriter, r *http.Request) {
	e.logins.Add(1)
	time.Sleep(e.loginDelay)

	if err := r.ParseForm(); err != nil {
		e.t.Errorf("token request: %v", err)
	}
	if r.Method != http.MethodPost || r.PostForm.Get("grant_type") != "password" ||
		r.PostForm.Get("scope") != "ovirt-app-api" || r.PostForm.Get("username") != "prometheus@internal" {
		e.t.Errorf("unexpected token request: %s %v", r.Method, r.PostForm)
	}

	w.Header().Set("Content-Type", "application/json")
	if e.failLogins.Load() || r.PostForm.Get("password") != "secret" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error_code": "access_denied", "error": "Cannot authenticate user"})
		return
	}

	json.NewEncoder(w).Encode(map[string]string{"access_token": e.issue(), "token_type": "bearer", "scope": "ovirt-app-api"})
}

func (e *fakeEngine) handleAPI(w http.ResponseWriter, r *http.Request) {
	// persistent-auth login: HEAD on the API root with Basic credentials
	if user, pass, ok := r.BasicAuth(); ok {
		e.logins.Add(1)
		time.Sleep(e.loginDelay)
		if r.Method != http.MethodHead || r.Header.Get("Prefer") != "persistent-auth" {
			e.t.Errorf("unexpected Basic-auth request: %s %s", r.Method, r.URL)
		}
		if e.failLogins.Load() || user != "prometheus@internal" || pass != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "JSESSIONID", Value: e.issue(), Path: "/ovirt-engine/api"})
		return
	}

	e.apiHits.Add(1)
	n := e.inFlight.Add(1)
	defer e.inFlight.Add(-1)
	for {
		m := e.maxFlight.Load()
		if n <= m || e.maxFlight.CompareAndSwap(m, n) {
			break
		}
	}
	time.Sleep(e.apiDelay)

	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if ck, err := r.Cookie("JSESSIONID"); err == nil {
		got = ck.Value
	}

	e.mu.Lock()
	ok := got != "" && got == e.valid
	e.mu.Unlock()

	if !ok {
		if e.onUnauth != nil {
			e.onUnauth()
		}
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	w.Header().Set("Content-Type", "application/xml")
	fmt.Fprintf(w, `<vm id="%s"><name>vm</name></vm>`, strings.TrimPrefix(r.URL.Path, "/ovirt-engine/api/"))
}

type testVM struct {
	ID   string `xml:"id,attr"`
	Name string `xml:"name"`
}

func newTestClient(t *testing.T, e *fakeEngine, opts ...ClientOption) *Client {
	t.Helper()
	c, err := NewClient(e.apiURL(), "prometheus@internal", "secret", opts...)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

// runConcurrent fires n GetAndParse calls at once and returns their errors
func runConcurrent(c *Client, n int) []error {
	errs := make([]error, n)
	start := make(chan struct{})
	wg := sync.WaitGroup{}
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			var v testVM
			errs[i] = c.GetAndParse(context.Background(), fmt.Sprintf("vms/%d/snapshots", i), &v)
		}(i)
	}
	close(start)
	wg.Wait()
	return errs
}

// The incident: the session expires and every in-flight per-VM request gets a
// 401 in the same instant. Exactly one re-login may reach the engine; every
// request waits for it and retries once.
func TestConcurrent401sTriggerExactlyOneLogin(t *testing.T) {
	const n = 64

	for _, method := range []AuthMethod{AuthOAuth, AuthSession} {
		t.Run(string(method), func(t *testing.T) {
			e := newFakeEngine(t)
			e.loginDelay = 50 * time.Millisecond

			// all n requests must be in flight with the expired credential and
			// receive their 401 together, before any of them can log in again
			var arrived atomic.Int32
			released := make(chan struct{})
			var once sync.Once
			e.onUnauth = func() {
				if arrived.Add(1) == n {
					once.Do(func() { close(released) })
				}
				select {
				case <-released:
				case <-time.After(5 * time.Second):
				}
			}

			c := newTestClient(t, e, WithAuthMethod(method), WithMaxConcurrentRequests(n))
			if got := e.logins.Load(); got != 1 {
				t.Fatalf("initial logins = %d, want 1", got)
			}

			e.expire()
			for i, err := range runConcurrent(c, n) {
				if err != nil {
					t.Errorf("request %d: %v", i, err)
				}
			}

			if got := arrived.Load(); got != n {
				t.Errorf("requests that saw a 401 = %d, want %d", got, n)
			}
			if got := e.logins.Load() - 1; got != 1 {
				t.Errorf("re-logins after %d concurrent 401s = %d, want exactly 1", n, got)
			}
			if got := e.apiHits.Load(); got != 2*n {
				t.Errorf("API requests = %d, want %d (each request retried exactly once)", got, 2*n)
			}
		})
	}
}

// A request that arrives while a re-login is running waits for it instead of
// sending the stale credential.
func TestRequestsWaitForRunningLogin(t *testing.T) {
	e := newFakeEngine(t)
	c := newTestClient(t, e)

	e.expire()
	e.loginDelay = 100 * time.Millisecond

	errs := make(chan error, 2)
	go func() {
		var v testVM
		errs <- c.GetAndParse(context.Background(), "vms/a", &v)
	}()

	// wait until the first request has triggered the re-login
	deadline := time.Now().Add(2 * time.Second)
	for e.logins.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	hitsBefore := e.apiHits.Load()

	go func() {
		var v testVM
		errs <- c.GetAndParse(context.Background(), "vms/b", &v)
	}()

	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Errorf("request: %v", err)
		}
	}

	if got := e.logins.Load(); got != 2 {
		t.Errorf("logins = %d, want 2", got)
	}
	// the second request must not have gone out with the expired credential
	if got := e.apiHits.Load() - hitsBefore; got != 2 {
		t.Errorf("API requests after login started = %d, want 2", got)
	}
}

// After a failed login the client must not hammer the engine: concurrent
// requests share one attempt, and until the backoff expires requests fail
// without sending anything.
func TestFailedLoginBacksOff(t *testing.T) {
	e := newFakeEngine(t)

	var mu sync.Mutex
	now := time.Now()
	clock := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	advance := func(d time.Duration) {
		mu.Lock()
		now = now.Add(d)
		mu.Unlock()
	}

	c := newTestClient(t, e, withClock(clock), WithLoginBackoff(10*time.Second, 40*time.Second))

	e.expire()
	e.failLogins.Store(true)

	for i, err := range runConcurrent(c, 32) {
		if err == nil {
			t.Errorf("request %d succeeded although login fails", i)
		}
	}
	if got := e.logins.Load() - 1; got != 1 {
		t.Fatalf("login attempts after failure wave = %d, want 1", got)
	}

	// inside the backoff window: no login, no API request
	hits := e.apiHits.Load()
	advance(9 * time.Second)
	for _, err := range runConcurrent(c, 32) {
		if err == nil {
			t.Fatal("request succeeded during backoff")
		}
	}
	if got := e.logins.Load() - 1; got != 1 {
		t.Errorf("login attempts during backoff = %d, want 1", got)
	}
	if got := e.apiHits.Load(); got != hits {
		t.Errorf("API requests during backoff = %d, want 0", got-hits)
	}

	// backoff expired: one more attempt, which fails and doubles the backoff
	advance(2 * time.Second)
	runConcurrent(c, 8)
	if got := e.logins.Load() - 1; got != 2 {
		t.Errorf("login attempts after first backoff = %d, want 2", got)
	}
	advance(11 * time.Second) // 13s since the last attempt, backoff is now 20s
	runConcurrent(c, 8)
	if got := e.logins.Load() - 1; got != 2 {
		t.Errorf("login attempts before doubled backoff expired = %d, want 2", got)
	}

	// engine healthy again: the next attempt succeeds and requests work
	e.failLogins.Store(false)
	advance(10 * time.Second)
	for i, err := range runConcurrent(c, 8) {
		if err != nil {
			t.Errorf("request %d after recovery: %v", i, err)
		}
	}
	if got := e.logins.Load() - 1; got != 3 {
		t.Errorf("login attempts after recovery = %d, want 3", got)
	}
}

// No matter how many goroutines issue requests, at most maxRequests are in
// flight to the engine.
func TestConcurrencyCap(t *testing.T) {
	for _, limit := range []int{1, 3, DefaultMaxConcurrentRequests} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			e := newFakeEngine(t)
			e.apiDelay = 10 * time.Millisecond
			c := newTestClient(t, e, WithMaxConcurrentRequests(limit))

			for i, err := range runConcurrent(c, 100) {
				if err != nil {
					t.Errorf("request %d: %v", i, err)
				}
			}

			if got := e.maxFlight.Load(); got > int32(limit) {
				t.Errorf("max requests in flight = %d, want <= %d", got, limit)
			}
			if got := e.maxFlight.Load(); got < int32(limit) {
				t.Errorf("max requests in flight = %d, the cap of %d was never reached", got, limit)
			}
		})
	}
}

func TestDefaultsAndValidation(t *testing.T) {
	e := newFakeEngine(t)
	c := newTestClient(t, e)
	if c.authMethod != AuthOAuth {
		t.Errorf("default auth method = %q, want %q", c.authMethod, AuthOAuth)
	}
	if cap(c.sem) != 8 {
		t.Errorf("default concurrency = %d, want 8", cap(c.sem))
	}
	if c.ssoURL != e.server.URL+"/ovirt-engine/sso/oauth/token" {
		t.Errorf("ssoURL = %q", c.ssoURL)
	}

	if _, err := NewClient(e.apiURL(), "prometheus@internal", "secret", WithMaxConcurrentRequests(0)); err == nil {
		t.Error("max concurrent requests 0 accepted")
	}
	if _, err := NewClient(e.apiURL(), "prometheus@internal", "secret", WithAuthMethod("basic")); err == nil {
		t.Error("unknown auth method accepted")
	}
	if _, err := NewClient(e.apiURL(), "prometheus@internal", "wrong"); err == nil {
		t.Error("wrong password accepted")
	}
}

func TestParseExpiry(t *testing.T) {
	want := time.UnixMilli(1790000000000)
	for _, v := range []interface{}{"1790000000000", float64(1790000000000), "1790000000"} {
		if got := parseExpiry(v); !got.Equal(want) {
			t.Errorf("parseExpiry(%v) = %v, want %v", v, got, want)
		}
	}
	for _, v := range []interface{}{nil, "", "soon", float64(0)} {
		if got := parseExpiry(v); !got.IsZero() {
			t.Errorf("parseExpiry(%v) = %v, want zero", v, got)
		}
	}
}
