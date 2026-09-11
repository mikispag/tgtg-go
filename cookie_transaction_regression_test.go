package tgtg

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAuthenticationRedirectUsesStagedCookies(t *testing.T) {
	for _, valid := range []bool{true, false} {
		t.Run(map[bool]string{true: "valid", false: "invalid"}[valid], func(t *testing.T) {
			observed := make(chan string, 1)
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/"+RefreshEndpoint {
					w.Header().Add("Set-Cookie", "session=replaced; Path=/")
					w.Header().Add("Set-Cookie", "session=; Path=/api/; Max-Age=0")
					w.Header().Add("Set-Cookie", "challenge=issued; Path=/api/")
					http.Redirect(w, r, "/api/finish", http.StatusSeeOther)
					return
				}
				observed <- r.Header.Get("Cookie")
				if valid {
					_, _ = io.WriteString(w, `{"access_token":"new","refresh_token":"new-refresh"}`)
				} else {
					_, _ = io.WriteString(w, `{}`)
				}
			}))
			defer s.Close()
			c := New(Config{
				URL: s.URL + "/api/", UserAgent: "ua", Output: io.Discard,
				DataDomeSDKURL: s.URL + "/sdk",
				AccessToken:    "old", RefreshToken: "old-refresh", Cookie: "datadome=existing",
			})
			u, _ := url.Parse(c.BaseURL)
			c.httpClient.Jar.SetCookies(u, []*http.Cookie{
				{Name: "session", Value: "root", Path: "/"},
				{Name: "session", Value: "scoped", Path: "/api/"},
				{Name: "unrelated", Value: "keep", Path: "/"},
			})
			err := c.Login(context.Background())
			if (err == nil) != valid {
				t.Fatalf("Login error = %v, valid response = %v", err, valid)
			}
			request := &http.Request{Header: http.Header{"Cookie": {<-observed}}}
			if ck, err := request.Cookie("challenge"); err != nil || ck.Value != "issued" {
				t.Errorf("redirect did not receive challenge: %s", request.Header.Get("Cookie"))
			}
			if ck, err := request.Cookie("session"); err != nil || ck.Value != "replaced" {
				t.Errorf("redirect did not apply scoped deletion/replacement: %s", request.Header.Get("Cookie"))
			}
			final := &http.Request{Header: make(http.Header)}
			for _, ck := range c.httpClient.Jar.Cookies(u) {
				final.AddCookie(ck)
			}
			wantSession := "scoped"
			if valid {
				wantSession = "replaced"
			}
			if ck, err := final.Cookie("session"); err != nil || ck.Value != wantSession {
				t.Errorf("committed session = %v, error = %v, want %s", ck, err, wantSession)
			}
			if ck, err := final.Cookie("unrelated"); err != nil || ck.Value != "keep" {
				t.Errorf("unrelated session cookie lost: %v, %v", ck, err)
			}
			if _, err := final.Cookie("challenge"); (err == nil) != valid {
				t.Errorf("challenge commit status = %v, valid response = %v", err, valid)
			}
		})
	}
}

func TestAuthenticationCookieExpiryStartsAtReceipt(t *testing.T) {
	current := time.Now()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Set-Cookie", "short=lived; Path=/; Max-Age=1")
		_, _ = io.WriteString(w, `{"access_token":"new","refresh_token":"refresh"}`)
	}))
	defer s.Close()
	c := New(Config{
		URL: s.URL + "/", UserAgent: "ua", Output: io.Discard,
		DataDomeSDKURL: s.URL + "/sdk",
		Cookie:         "datadome=existing", Now: func() time.Time { return current },
	})
	resp, err := c.postAuth(context.Background(), s.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	current = current.Add(2 * time.Second)
	c.commitAuthCookies(resp)
	if strings.Contains(c.Cookie, "short=") {
		t.Fatalf("commit restarted an expired cookie's lifetime: %s", c.Cookie)
	}
}

func TestCookieSnapshotPreservesFirstValue(t *testing.T) {
	u, _ := url.Parse("https://example.test/api/")
	jar, _ := cookiejar.New(nil)
	jar.SetCookies(u, []*http.Cookie{
		{Name: "session", Value: "root", Path: "/"},
		{Name: "session", Value: "api", Path: "/api/"},
	})
	for _, cfg := range []Config{
		{HTTPClient: &http.Client{Jar: jar}},
		{Cookie: "session=api; session=root"},
		{HTTPClient: &http.Client{Jar: jar}, Cookie: "session=override; session=ignored"},
	} {
		cfg.URL, cfg.UserAgent, cfg.Output = u.String(), "ua", io.Discard
		c := New(cfg)
		want := "session=api"
		if strings.Contains(cfg.Cookie, "override") {
			want = "session=override"
		}
		if c.Cookie != want {
			t.Errorf("restored snapshot = %q, want %q", c.Cookie, want)
		}
	}
}

func TestOwnedCookieJarUsesInjectedExpiryClock(t *testing.T) {
	current := time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)
	c := New(Config{
		URL: "https://example.test/api/", UserAgent: "ua", Output: io.Discard,
		Now: func() time.Time { return current },
	})
	u, _ := url.Parse(c.BaseURL)
	c.httpClient.Jar.SetCookies(u, []*http.Cookie{
		{Name: "relative", Value: "two-seconds", MaxAge: 2},
		{Name: "absolute", Value: "one-second", Expires: current.Add(time.Second)},
	})
	if got := c.httpClient.Jar.Cookies(u); len(got) != 2 {
		t.Fatalf("native wall clock rejected live historical-clock cookies: %v", got)
	}
	current = current.Add(time.Second)
	if got := c.httpClient.Jar.Cookies(u); len(got) != 1 || got[0].Name != "relative" {
		t.Fatalf("absolute expiry did not follow injected clock: %v", got)
	}
	current = current.Add(time.Second)
	if got := c.httpClient.Jar.Cookies(u); len(got) != 0 {
		t.Fatalf("relative expiry restarted during materialization: %v", got)
	}
}

func TestOwnedCookieJarMatchesStandardScopes(t *testing.T) {
	c := New(Config{URL: "https://sub.example.test/api/", UserAgent: "ua", Output: io.Discard})
	reference, _ := cookiejar.New(nil)
	steps := []struct {
		origin string
		cookie http.Cookie
	}{
		{"https://sub.example.test/api/start", http.Cookie{Name: "session", Value: "parent", Domain: ".example.test", Path: "/"}},
		{"https://sub.example.test/api/start", http.Cookie{Name: "session", Value: "host", Path: "/"}},
		{"https://sub.example.test/api/start", http.Cookie{Name: "default", Value: "path"}},
		{"https://sub.example.test/api/start", http.Cookie{Name: "secure", Value: "only", Secure: true, Path: "/"}},
		{"https://example.test/api/start", http.Cookie{Name: "session", Value: "parent-replaced", Path: "/"}},
		{"https://sub.example.test/api/start", http.Cookie{Name: "session", Value: "invalid", Domain: "..example.test", Path: "/"}},
		{"https://sub.example.test/api/start", http.Cookie{Name: "session", Domain: "foreign.test", Path: "/", MaxAge: -1}},
		{"https://sub.example.test/api/start", http.Cookie{Name: "session", Domain: "EXAMPLE.TEST", Path: "/", MaxAge: -1}},
		{"https://sub.example.test/api/start", http.Cookie{Name: "session", Value: "parent-recreated", Domain: "example.test", Path: "/"}},
		{"https://bücher.test/api/start", http.Cookie{Name: "idn", Value: "original", Path: "/"}},
		{"https://xn--bcher-kva.test/api/start", http.Cookie{Name: "idn", Value: "replacement", Path: "/"}},
	}
	for i, step := range steps {
		u, _ := url.Parse(step.origin)
		c.httpClient.Jar.SetCookies(u, []*http.Cookie{&step.cookie})
		reference.SetCookies(u, []*http.Cookie{&step.cookie})
		for _, target := range []string{
			"https://sub.example.test/api/child", "http://sub.example.test/api/child",
			"https://example.test/", "https://other.example.test/api/child",
			"https://sub.example.test/other", "https://bücher.test/", "https://xn--bcher-kva.test/",
		} {
			requestURL, _ := url.Parse(target)
			got, want := c.httpClient.Jar.Cookies(requestURL), reference.Cookies(requestURL)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("step %d, URL %s: cookies = %v, want %v", i, target, got, want)
			}
		}
	}
}
