package tgtg

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
)

func TestSeededHTTPSCookiesStaySecure(t *testing.T) {
	apiURL, _ := url.Parse("https://api.example.test/api/")
	plainURL, _ := url.Parse("http://api.example.test/api/")
	jar, _ := cookiejar.New(nil)
	jar.SetCookies(apiURL, []*http.Cookie{{
		Name: "session", Value: "secret", Path: "/", Secure: true,
	}})
	c := New(Config{
		URL: apiURL.String(), UserAgent: "test-agent",
		HTTPClient: &http.Client{Jar: jar}, Output: io.Discard,
	})
	if got := c.httpClient.Jar.Cookies(plainURL); len(got) != 0 {
		t.Fatalf("secure session became available over HTTP: %v", got)
	}
	if got := c.httpClient.Jar.Cookies(apiURL); len(got) != 1 || got[0].Value != "secret" {
		t.Fatalf("HTTPS session was not copied: %v", got)
	}
	if got := jar.Cookies(apiURL); len(got) != 1 || got[0].Value != "secret" {
		t.Fatalf("supplied jar was modified: %v", got)
	}
}

type cookieScopeTransport func(*http.Request) (*http.Response, error)

func (f cookieScopeTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestDataDomeRetryClearsScopedCookies(t *testing.T) {
	for _, origin := range []string{
		"https://api.example.test", "https://127.0.0.1", "https://[::1]",
	} {
		t.Run(origin, func(t *testing.T) {
			requestURL, _ := url.Parse(origin + "/api/item/v9/42/")
			attempts, sdkCalls := 0, 0
			transport := cookieScopeTransport(func(r *http.Request) (*http.Response, error) {
				status, body := http.StatusOK, `{}`
				if r.URL.Host == "sdk.example.test" {
					sdkCalls++
					body = `{"status":200,"cookie":"datadome=fresh; Path=/; Max-Age=3600"}`
				} else {
					attempts++
					if attempts == 1 {
						status = http.StatusForbidden
					} else {
						dataDomeCookies := 0
						for _, ck := range r.Cookies() {
							if ck.Name == "datadome" {
								dataDomeCookies++
								if ck.Value != "fresh" {
									t.Errorf("retry retained stale cookie: %s", r.Header.Get("Cookie"))
								}
							}
						}
						if dataDomeCookies != 1 {
							t.Errorf("retry has %d DataDome cookies, want 1", dataDomeCookies)
						}
						if ck, err := r.Cookie("session"); err != nil || ck.Value != "keep" {
							t.Errorf("retry lost session cookie: %v, %v", ck, err)
						}
					}
				}
				return &http.Response{
					StatusCode: status, Header: make(http.Header),
					Body: io.NopCloser(strings.NewReader(body)), Request: r,
				}, nil
			})
			c := New(Config{
				URL: origin + "/api/", UserAgent: "test-agent", Output: io.Discard,
				DataDomeSDKURL: "https://sdk.example.test/",
				HTTPClient:     &http.Client{Transport: transport},
			})
			for _, path := range []string{
				"/", "/api", "/api/", "/api/item", "/api/item/",
				"/api/item/v9", "/api/item/v9/", "/api/item/v9/42", "/api/item/v9/42/",
			} {
				c.httpClient.Jar.SetCookies(requestURL, []*http.Cookie{{
					Name: "datadome", Value: "stale", Path: path, Secure: true,
				}})
			}
			if requestURL.Hostname() == "api.example.test" {
				c.httpClient.Jar.SetCookies(requestURL, []*http.Cookie{{
					Name: "datadome", Value: "parent-stale", Domain: "example.test", Path: "/api/",
				}})
			}
			c.httpClient.Jar.SetCookies(requestURL, []*http.Cookie{
				{Name: "session", Value: "keep", Path: "/api/", Secure: true},
				{Name: "datadome", Value: "unrelated", Path: "/other/"},
			})
			if _, err := c.post(context.Background(), requestURL.String(), nil); err != nil {
				t.Fatal(err)
			}
			if attempts != 2 || sdkCalls != 1 {
				t.Fatalf("API attempts = %d, SDK calls = %d; want 2 and 1", attempts, sdkCalls)
			}
			otherURL, _ := url.Parse(origin + "/other/")
			found := false
			for _, ck := range c.httpClient.Jar.Cookies(otherURL) {
				found = found || ck.Name == "datadome" && ck.Value == "unrelated"
			}
			if !found {
				t.Fatal("retry removed DataDome cookie for an unrelated path")
			}
		})
	}
}
