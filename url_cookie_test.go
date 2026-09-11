package tgtg

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestOptionalSlashKeepsAPICookies(t *testing.T) {
	for _, base := range []string{"/api", "/api/"} {
		t.Run(base, func(t *testing.T) {
			seen := make(chan string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				value := ""
				if ck, err := r.Cookie("session"); err == nil {
					value = ck.Value
				}
				seen <- value
				io.WriteString(w, `{}`)
			}))
			defer server.Close()
			jar, _ := cookiejar.New(nil)
			origin, _ := url.Parse(server.URL + "/api/")
			jar.SetCookies(origin, []*http.Cookie{{Name: "session", Value: "existing", Path: "/api/"}})
			c := New(Config{URL: server.URL + base, DataDomeSDKURL: server.URL + "/sdk", HTTPClient: &http.Client{Jar: jar}, UserAgent: "ua", Output: io.Discard, Cookie: "datadome=valid", AccessToken: "a", RefreshToken: "r", LastTimeTokenRefreshed: time.Now()})
			if _, err := c.GetItem(context.Background(), "item"); err != nil {
				t.Fatal(err)
			}
			if got := <-seen; got != "existing" {
				t.Fatalf("base URL %s lost applicable session cookie: %q", base, got)
			}
		})
	}
}
func TestOptionalSlashPersistsAPICookies(t *testing.T) {
	for _, base := range []string{"/api", "/api/"} {
		t.Run(base, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Set-Cookie", "session=fresh; Path=/api/")
				io.WriteString(w, `{"access_token":"a","refresh_token":"r"}`)
			}))
			defer server.Close()
			c := New(Config{URL: server.URL + base, DataDomeSDKURL: server.URL + "/sdk", UserAgent: "ua", Output: io.Discard, Cookie: "datadome=valid", AccessToken: "old", RefreshToken: "old"})
			creds, err := c.GetCredentials(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			request := &http.Request{Header: http.Header{"Cookie": {creds.Cookie}}}
			if ck, err := request.Cookie("session"); err != nil || ck.Value != "fresh" {
				t.Fatalf("base URL %s omitted refreshed session: cookies=%q err=%v", base, creds.Cookie, err)
			}
		})
	}
}
