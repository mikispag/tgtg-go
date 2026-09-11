package tgtg

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRefreshPreservesCookieWithoutSetCookie(t *testing.T) {
	m := newMockServer(t)
	m.addJSON(http.MethodPost, "/"+RefreshEndpoint, http.StatusOK,
		map[string]any{"access_token": "new-access", "refresh_token": "new-refresh"}, nil)
	m.addJSON(http.MethodPost, "/"+APIItemEndpoint, http.StatusOK,
		map[string]any{"items": []any{}}, nil)
	cfg := fakeTokensConfig()
	c := newClient(t, m, cfg)
	for poll := 0; poll < 2; poll++ {
		if _, err := c.GetItems(context.Background(), DefaultGetItemsOptions()); err != nil {
			t.Fatalf("poll %d after refresh without Set-Cookie: %v", poll+1, err)
		}
	}
	if c.Cookie != cfg.Cookie {
		t.Fatal("refresh discarded the existing cookie")
	}
}

func TestSDKCookieReplacesSavedCookieAndSurvivesRestart(t *testing.T) {
	m := newMockServer(t)
	sdkCalls := m.addJSON(http.MethodPost, "/datadome", http.StatusOK,
		map[string]any{"status": 200, "cookie": "datadome=new; Path=/"}, nil)
	var refreshCalls atomic.Int32
	m.routes = append(m.routes, &route{method: http.MethodPost, path: "/" + RefreshEndpoint, calls: new(int64),
		handler: func(w http.ResponseWriter, r *http.Request) {
			if refreshCalls.Add(1) == 1 {
				if got := r.Header.Get("Cookie"); got != "datadome=old" {
					t.Errorf("initial cookie = %q, want saved cookie", got)
				}
				w.WriteHeader(http.StatusForbidden)
				return
			}
			if got := r.Header.Get("Cookie"); got != "datadome=new" {
				t.Errorf("retry cookie = %q, want exactly one refreshed cookie", got)
			}
			fmt.Fprint(w, `{"access_token":"new-access","refresh_token":"new-refresh"}`)
		}})
	m.routes = append(m.routes, &route{method: http.MethodPost, path: "/" + APIItemEndpoint, calls: new(int64),
		handler: func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get("Cookie"); got != "datadome=new" {
				t.Errorf("items cookie = %q, want refreshed cookie", got)
			}
			fmt.Fprint(w, `{"items":[]}`)
		}})
	cfg := fakeTokensConfig()
	cfg.Cookie = "datadome=old"
	c := newClient(t, m, cfg)
	if _, err := c.GetItems(context.Background(), DefaultGetItemsOptions()); err != nil {
		t.Fatal(err)
	}
	if c.Cookie != "datadome=new" {
		t.Fatalf("persistable cookie = %q, want refreshed cookie", c.Cookie)
	}
	cfg.AccessToken, cfg.RefreshToken, cfg.Cookie = c.AccessToken, c.RefreshToken, c.Cookie
	cfg.LastTimeTokenRefreshed = c.LastTimeTokenRefreshed
	restarted := newClient(t, m, cfg)
	if _, err := restarted.GetItems(context.Background(), DefaultGetItemsOptions()); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt64(sdkCalls); got != 1 {
		t.Fatalf("SDK requests = %d, want one refresh across restart", got)
	}
}

func TestSDKCookiePersistsAlongsideResponseCookies(t *testing.T) {
	m := newMockServer(t)
	m.addJSON(http.MethodPost, "/datadome", http.StatusOK,
		map[string]any{"status": 200, "cookie": "datadome=new; Path=/"}, nil)
	m.addJSON(http.MethodPost, "/"+RefreshEndpoint, http.StatusOK,
		map[string]any{"access_token": "new-access", "refresh_token": "new-refresh"},
		http.Header{"Set-Cookie": {"session=rotated; Path=/; HttpOnly"}})
	cfg := fakeTokensConfig()
	cfg.Cookie = "session=old"
	c := newClient(t, m, cfg)
	if err := c.Login(context.Background()); err != nil {
		t.Fatal(err)
	}
	request := &http.Request{Header: http.Header{"Cookie": {c.Cookie}}}
	got := make(map[string]string)
	for _, cookie := range request.Cookies() {
		got[cookie.Name] = cookie.Value
	}
	if len(request.Cookies()) != 2 || got["session"] != "rotated" || got["datadome"] != "new" || strings.Contains(c.Cookie, "Path") {
		t.Fatalf("persistable cookie lost SDK state or retained response attributes: %q", c.Cookie)
	}
}

func TestSavedCookieNormalizesLegacySetCookieSnapshot(t *testing.T) {
	m := newMockServer(t)
	cfg := fakeTokensConfig()
	cfg.Cookie = "session=abc; Path=/; Expires=Wed, 21 Oct 2030 07:28:00 GMT, datadome=ddv; Path=/; Secure; HttpOnly; SameSite=Lax"
	c := newClient(t, m, cfg)
	if c.Cookie != "session=abc; datadome=ddv" {
		t.Fatalf("legacy snapshot was not normalized: %q", c.Cookie)
	}
}

func TestResponseCookieReplacesSavedCookieWithAPIPathPrefix(t *testing.T) {
	m := newMockServer(t)
	m.addJSON(http.MethodPost, "/api/"+RefreshEndpoint, http.StatusOK,
		map[string]any{"access_token": "new-access", "refresh_token": "new-refresh"},
		http.Header{"Set-Cookie": {"datadome=new; Path=/"}})
	m.routes = append(m.routes, &route{method: http.MethodPost, path: "/api/" + APIItemEndpoint, calls: new(int64),
		handler: func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get("Cookie"); got != "datadome=new" {
				t.Errorf("cookie = %q, want exactly one refreshed cookie", got)
			}
			fmt.Fprint(w, `{"items":[]}`)
		}})
	cfg := fakeTokensConfig()
	cfg.URL = m.server.URL + "/api/"
	cfg.Cookie = "datadome=old"
	c := newClient(t, m, cfg)
	if _, err := c.GetItems(context.Background(), DefaultGetItemsOptions()); err != nil {
		t.Fatal(err)
	}
	if c.Cookie != "datadome=new" {
		t.Fatalf("persistable cookie = %q, want exactly one refreshed cookie", c.Cookie)
	}
}

func TestSDKFailureAfterForbiddenRemainsRetryable(t *testing.T) {
	m := newMockServer(t)
	var sdkAvailable atomic.Bool
	m.routes = append(m.routes, &route{method: http.MethodPost, path: "/datadome", calls: new(int64),
		handler: func(w http.ResponseWriter, r *http.Request) {
			if !sdkAvailable.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			fmt.Fprint(w, `{"status":200,"cookie":"datadome=new; Path=/"}`)
		}})
	m.routes = append(m.routes, &route{method: http.MethodPost, path: "/" + RefreshEndpoint, calls: new(int64),
		handler: func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie("datadome")
			if err != nil || cookie.Value != "new" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			fmt.Fprint(w, `{"access_token":"new-access","refresh_token":"new-refresh"}`)
		}})
	cfg := fakeTokensConfig()
	cfg.Cookie = "datadome=old"
	c := newClient(t, m, cfg)
	if err := c.Login(context.Background()); err == nil {
		t.Fatal("expected initial failure while SDK is unavailable")
	}
	sdkAvailable.Store(true)
	if err := c.Login(context.Background()); err != nil {
		t.Fatalf("failed to recover after SDK became available: %v", err)
	}
}

func TestRefreshRejectsIncompleteTokensWithoutLosingCredentials(t *testing.T) {
	for _, body := range []string{`{}`, `null`, `{"access_token":"new"}`, `{"refresh_token":"new"}`, `{"access_token":"new","refresh_token":""}`, `{"access_token":" ","refresh_token":"new"}`} {
		t.Run(body, func(t *testing.T) {
			m := newMockServer(t)
			m.routes = append(m.routes, &route{method: http.MethodPost, path: "/" + RefreshEndpoint, calls: new(int64),
				handler: func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }})
			cfg := fakeTokensConfig()
			cfg.Cookie = "session=known"
			cfg.LastTimeTokenRefreshed = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
			c := newClient(t, m, cfg)
			var apiErr *APIError
			if err := c.Login(context.Background()); !errors.As(err, &apiErr) {
				t.Fatalf("refresh error = %v, want APIError", err)
			}
			if c.AccessToken != cfg.AccessToken || c.RefreshToken != cfg.RefreshToken || c.Cookie != cfg.Cookie || !c.LastTimeTokenRefreshed.Equal(cfg.LastTimeTokenRefreshed) {
				t.Fatal("invalid refresh response changed existing credentials")
			}
		})
	}
}
