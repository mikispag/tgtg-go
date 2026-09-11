package tgtg

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDataDomeCompressedResponse(t *testing.T) {
	m := newMockServer(t)
	payload := gzipBytes(t, []byte(`{"status":200,"cookie":"datadome=fresh; Max-Age=3600"}`))
	m.routes = append(m.routes, &route{method: http.MethodPost, path: "/datadome", calls: new(int64), handler: func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Accept-Encoding"), "br") {
			t.Error("SDK must not advertise unsupported compression")
		}
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(payload)
	}})
	c := newClient(t, m, fakeTokensConfig())
	c.fetchDataDomeCookie(context.Background(), m.baseURL())
	u, _ := url.Parse(m.baseURL())
	for _, ck := range c.httpClient.Jar.Cookies(u) {
		if ck.Name == "datadome" && ck.Value == "fresh" {
			return
		}
	}
	t.Fatalf("compressed SDK cookie not stored: %q", c.Cookie)
}

func TestDataDomeRejectsInvalidCookie(t *testing.T) {
	for _, cookie := range []string{"datadome=; Path=/", "session=value", "not a cookie"} {
		t.Run(cookie, func(t *testing.T) {
			m := newMockServer(t)
			m.addJSON(http.MethodPost, "/datadome", http.StatusOK, map[string]any{"status": 200, "cookie": cookie}, nil)
			c := newClient(t, m, fakeTokensConfig())
			c.fetchDataDomeCookie(context.Background(), m.baseURL())
			if c.Cookie != "session=original" {
				t.Fatalf("invalid SDK cookie changed session: %q", c.Cookie)
			}
		})
	}
}

func TestCookieCredentialsRoundTrip(t *testing.T) {
	seen := make(chan []*http.Cookie, 1)
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/"+RefreshEndpoint {
			w.Header().Add("Set-Cookie", "session=abc; Path=/; Secure; HttpOnly")
			w.Header().Add("Set-Cookie", "datadome=ddv; Path=/; Secure; Expires=Wed, 01 Jan 2031 00:00:00 GMT")
			_, _ = io.WriteString(w, `{"access_token":"a","refresh_token":"r"}`)
			return
		}
		seen <- r.Cookies()
		_, _ = io.WriteString(w, `{}`)
	}))
	defer s.Close()
	cfg := Config{URL: s.URL + "/", HTTPClient: s.Client(), UserAgent: "ua", Output: io.Discard,
		AccessToken: "old", RefreshToken: "old", Cookie: "datadome=old"}
	c := New(cfg)
	creds, err := c.GetCredentials(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(creds.Cookie, ",") || strings.Contains(creds.Cookie, "Path=") {
		t.Fatalf("saved credentials contain response cookie attributes: %q", creds.Cookie)
	}
	cfg.AccessToken, cfg.RefreshToken, cfg.Cookie = creds.AccessToken, creds.RefreshToken, creds.Cookie
	restored := New(cfg)
	if _, err := restored.doPost(context.Background(), s.URL+"/check", nil); err != nil {
		t.Fatal(err)
	}
	got := <-seen
	values := make(map[string]string)
	for _, ck := range got {
		values[ck.Name] = ck.Value
	}
	if len(got) != 2 || values["session"] != "abc" || values["datadome"] != "ddv" {
		t.Fatalf("restored cookies: %v", got)
	}
}

func TestDataDomeRetryReplacesOnlyRejectedCookie(t *testing.T) {
	seen := make(chan []*http.Cookie, 2)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sdk" {
			_ = json.NewEncoder(w).Encode(map[string]any{"status": 200, "cookie": "datadome=fresh; Max-Age=3600"})
			return
		}
		seen <- r.Cookies()
		ck, err := r.Cookie("datadome")
		if err != nil || ck.Value != "fresh" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	defer s.Close()
	c := New(Config{URL: s.URL + "/", DataDomeSDKURL: s.URL + "/sdk", UserAgent: "ua", Cookie: "datadome=stale; session=retained", Output: io.Discard})
	jar := c.httpClient.Jar
	resp, err := c.post(context.Background(), s.URL+"/api", nil)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("retry: status=%d err=%v", resp.StatusCode, err)
	}
	if c.httpClient.Jar != jar {
		t.Fatal("retry replaced the entire jar")
	}
	for _, want := range []string{"stale", "fresh"} {
		cookies := <-seen
		values := make(map[string]string)
		for _, ck := range cookies {
			values[ck.Name] = ck.Value
		}
		if len(cookies) != 2 || values["datadome"] != want || values["session"] != "retained" {
			t.Fatalf("want datadome=%s and retained session, got %v", want, cookies)
		}
	}
}

func TestRefreshWithoutCookieUpdate(t *testing.T) {
	m := newMockServer(t)
	m.addJSON(http.MethodPost, "/"+RefreshEndpoint, http.StatusOK, map[string]any{"access_token": "a", "refresh_token": "r"}, nil)
	c := newClient(t, m, fakeTokensConfig())
	for i := 0; i < 2; i++ {
		if err := c.Login(context.Background()); err != nil {
			t.Fatal(err)
		}
		if c.Cookie != "session=original" {
			t.Fatalf("refresh discarded saved cookie: %q", c.Cookie)
		}
	}
}

func TestHTTPClientSessionIsolation(t *testing.T) {
	seen := make(chan string, 1)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/alice" {
			w.Header().Set("Set-Cookie", "session=alice; Path=/")
		} else {
			seen <- r.Header.Get("Cookie")
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	defer s.Close()
	shared := &http.Client{Timeout: 13 * time.Second}
	cfg := Config{URL: s.URL + "/", UserAgent: "ua", HTTPClient: shared, Output: io.Discard}
	a := New(cfg)
	cfg.Cookie = "datadome=bob"
	cfg.Timeout = 2 * time.Second
	b := New(cfg)
	if _, err := a.doPost(context.Background(), s.URL+"/alice", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := b.doPost(context.Background(), s.URL+"/bob", nil); err != nil {
		t.Fatal(err)
	}
	if got := <-seen; got != "datadome=bob" {
		t.Fatalf("Bob received another client's cookies: %q", got)
	}
	if shared.Jar != nil || shared.Timeout != 13*time.Second || a.httpClient.Timeout != 13*time.Second || b.httpClient.Timeout != 2*time.Second {
		t.Fatal("HTTP client ownership/timeout settings changed")
	}
	jar, _ := cookiejar.New(nil)
	u, _ := url.Parse(s.URL)
	jar.SetCookies(u, []*http.Cookie{{Name: "session", Value: "caller", Path: "/"}})
	shared.Jar = jar
	c := New(cfg)
	if _, err := c.doPost(context.Background(), s.URL+"/alice", nil); err != nil {
		t.Fatal(err)
	}
	if got := jar.Cookies(u); len(got) != 1 || got[0].Value != "caller" {
		t.Fatalf("caller-owned jar was changed: %v", got)
	}
}

func TestAuthenticationRejectsIncompleteTokens(t *testing.T) {
	for _, flow := range []string{"refresh", "pin", "polling"} {
		for _, body := range []any{nil, map[string]any{}, map[string]any{"access_token": "a"}, map[string]any{"refresh_token": "r"}, map[string]any{"access_token": "", "refresh_token": "r"}} {
			t.Run(flow+"/"+fmtBody(body), func(t *testing.T) {
				m := newMockServer(t)
				cfg := fakeTokensConfig()
				cfg.PinReader = func() (string, error) { return "", nil }
				c := newClient(t, m, cfg)
				var err error
				switch flow {
				case "refresh":
					m.addJSON(http.MethodPost, "/"+RefreshEndpoint, http.StatusOK, body, nil)
					err = c.refreshToken(context.Background())
				case "pin":
					m.addJSON(http.MethodPost, "/"+AuthByRequestPinEndpoint, http.StatusOK, body, nil)
					err = c.authByPIN(context.Background(), "pid", "1234")
				case "polling":
					m.addJSON(http.MethodPost, "/"+AuthPollingEndpoint, http.StatusOK, body, nil)
					err = c.startPolling(context.Background(), "pid")
				}
				if err == nil || c.AccessToken != cfg.AccessToken || c.RefreshToken != cfg.RefreshToken || !c.LastTimeTokenRefreshed.IsZero() {
					t.Fatalf("invalid response accepted or tokens changed: err=%v", err)
				}
			})
		}
	}
}

func fmtBody(body any) string {
	b, _ := json.Marshal(body)
	return string(b)
}

func TestLoginCancellationDuringCallbacks(t *testing.T) {
	for _, mode := range []string{"legacy PIN", "context PIN", "legacy sleep", "context sleep"} {
		t.Run(mode, func(t *testing.T) {
			m := newMockServer(t)
			m.addJSON(http.MethodPost, "/"+AuthByEmailEndpoint, http.StatusOK, map[string]any{"state": "WAIT", "polling_id": "pid"}, nil)
			m.addJSON(http.MethodPost, "/"+AuthPollingEndpoint, http.StatusAccepted, nil, nil)
			entered, release := make(chan struct{}), make(chan struct{})
			defer close(release)
			cfg := Config{Email: "a@example.test", PinReader: func() (string, error) { return "", nil }}
			switch mode {
			case "legacy PIN":
				cfg.PinReader = func() (string, error) { close(entered); <-release; return "1234", nil }
			case "context PIN":
				cfg.PinReaderContext = func(ctx context.Context) (string, error) { close(entered); <-ctx.Done(); return "", ctx.Err() }
			case "legacy sleep":
				cfg.Sleep = func(time.Duration) { close(entered); <-release }
			case "context sleep":
				cfg.SleepContext = func(ctx context.Context, d time.Duration) error { close(entered); return sleepWithContext(ctx, d) }
			}
			c := newClient(t, m, cfg)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- c.Login(ctx) }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("callback did not start")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("login remained blocked after cancellation")
			}
		})
	}
}

func TestCanceledPINReaderIsReused(t *testing.T) {
	var calls atomic.Int32
	entered, release := make(chan struct{}, 1), make(chan struct{})
	c := New(Config{UserAgent: "ua", Output: io.Discard, PinReader: func() (string, error) {
		calls.Add(1)
		entered <- struct{}{}
		<-release
		return "1234", nil
	}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := c.readPIN(ctx); done <- err }()
	<-entered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := c.readPIN(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	close(release)
	pin, err := c.readPIN(context.Background())
	if err != nil || pin != "1234" || calls.Load() != 1 {
		t.Fatalf("pending input was not reused: pin=%q err=%v calls=%d", pin, err, calls.Load())
	}
}

func TestDefaultPollingTimerCancellation(t *testing.T) {
	c := New(Config{UserAgent: "ua", Output: io.Discard})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.wait(ctx, time.Hour) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("default timer ignored cancellation")
	}
}

func TestLoginRejectsMissingPollingID(t *testing.T) {
	m := newMockServer(t)
	m.addJSON(http.MethodPost, "/"+AuthByEmailEndpoint, http.StatusOK, map[string]any{"state": "WAIT"}, nil)
	var called atomic.Bool
	c := newClient(t, m, Config{Email: "a@example.test", PinReader: func() (string, error) {
		called.Store(true)
		return "1234", nil
	}})
	var loginErr *LoginError
	if err := c.Login(context.Background()); !errors.As(err, &loginErr) || loginErr.StatusCode != http.StatusOK || called.Load() {
		t.Fatalf("missing polling ID was not rejected before input: %v", err)
	}
}
