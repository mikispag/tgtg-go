package tgtg

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestInvalidAuthenticationPreservesSessionCookies(t *testing.T) {
	ctx := context.Background()
	flows := []struct {
		name string
		path string
		call func(*Client) error
	}{
		{"refresh", RefreshEndpoint, func(c *Client) error { return c.refreshToken(ctx) }},
		{"PIN", AuthByRequestPinEndpoint, func(c *Client) error { return c.authByPIN(ctx, "pid", "1234") }},
		{"polling", AuthPollingEndpoint, func(c *Client) error { return c.startPolling(ctx, "pid") }},
		{"signup", SignupByEmailEndpoint, func(c *Client) error { return c.SignupByEmail(ctx, DefaultSignupOptions("a@example.test")) }},
		{"email", AuthByEmailEndpoint, func(c *Client) error { return c.Login(ctx) }},
	}
	for _, flow := range flows {
		for _, update := range []string{"session=bad; Path=/", "session=; Max-Age=-1; Path=/"} {
			t.Run(flow.name+"/"+update, func(t *testing.T) {
				m := newMockServer(t)
				m.addJSON(http.MethodPost, "/"+flow.path, http.StatusOK, map[string]any{}, http.Header{"Set-Cookie": {update}})
				seen := make(chan string, 1)
				m.routes = append(m.routes, &route{method: http.MethodPost, path: "/probe", calls: new(int64), handler: func(w http.ResponseWriter, r *http.Request) {
					cookie, err := r.Cookie("session")
					if err != nil {
						seen <- ""
					} else {
						seen <- cookie.Value
					}
					_, _ = io.WriteString(w, `{}`)
				}})
				cfg := fakeTokensConfig()
				cfg.Cookie = "session=valid; datadome=valid"
				cfg.PinReader = func() (string, error) { return "", nil }
				if flow.name == "email" {
					cfg.Email, cfg.AccessToken, cfg.RefreshToken = "a@example.test", "", ""
				}
				c := newClient(t, m, cfg)
				if err := flow.call(c); err == nil {
					t.Fatal("invalid auth response accepted")
				}
				if c.Cookie != cfg.Cookie || c.AccessToken != cfg.AccessToken || c.RefreshToken != cfg.RefreshToken {
					t.Errorf("invalid auth response changed persisted credentials: cookie=%q", c.Cookie)
				}
				if _, err := c.doPost(ctx, m.server.URL+"/probe", nil); err != nil {
					t.Fatal(err)
				}
				if got := <-seen; got != "valid" {
					t.Errorf("invalid auth response changed cookies on the wire: session=%q", got)
				}
			})
		}
	}
}

func TestAuthenticationRetryDiscardsRejectedCookieUpdates(t *testing.T) {
	var attempts atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sdk" {
			_, _ = io.WriteString(w, `{"status":200,"cookie":"datadome=fresh"}`)
			return
		}
		session, err := r.Cookie("session")
		if err != nil || session.Value != "valid" {
			t.Errorf("unvalidated cookies sent on auth attempt: %q", r.Header.Get("Cookie"))
		}
		if attempts.Add(1) == 1 {
			w.Header().Set("Set-Cookie", "session=bad; Path=/")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Set-Cookie", "session=accepted; Path=/")
		_, _ = io.WriteString(w, `{"access_token":"a","refresh_token":"r"}`)
	}))
	defer s.Close()
	c := New(Config{URL: s.URL + "/", DataDomeSDKURL: s.URL + "/sdk", UserAgent: "ua", Output: io.Discard,
		AccessToken: "old", RefreshToken: "old", Cookie: "session=valid; datadome=stale"})
	if err := c.Login(context.Background()); err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 2 || !strings.Contains(c.Cookie, "session=accepted") || c.AccessToken != "a" {
		t.Fatalf("validated response was not committed: attempts=%d cookie=%q", attempts.Load(), c.Cookie)
	}
}

func TestAuthenticationReadFailurePreservesCookies(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Set-Cookie", "session=bad; Path=/")
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = io.WriteString(w, "invalid gzip")
	}))
	defer s.Close()
	c := New(Config{URL: s.URL + "/", UserAgent: "ua", Output: io.Discard,
		AccessToken: "old", RefreshToken: "old", Cookie: "session=valid; datadome=valid"})
	if err := c.Login(context.Background()); err == nil {
		t.Fatal("corrupt response accepted")
	}
	c.syncCookies()
	if c.Cookie != "session=valid; datadome=valid" || c.AccessToken != "old" {
		t.Fatalf("body read failure changed credentials: cookie=%q", c.Cookie)
	}
}
