package tgtg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestResponseErrorsUnwrap(t *testing.T) {
	cause := errors.New("invalid response")
	for _, err := range []error{
		&LoginError{StatusCode: http.StatusOK, Body: "{}", Err: cause},
		&APIError{StatusCode: http.StatusOK, Body: "{}", Err: cause},
		&APIError{StatusCode: http.StatusOK, State: "FAILURE", Err: cause},
	} {
		if !errors.Is(err, cause) || !strings.Contains(err.Error(), cause.Error()) {
			t.Errorf("error must expose its cause: %v", err)
		}
	}
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&LoginError{StatusCode: 401, Body: "bad"}, "tgtg login error: status 401: bad"},
		{&APIError{StatusCode: 500, Body: "bad"}, "tgtg API error: status 500: bad"},
		{&APIError{StatusCode: 200, State: "FAILURE", Body: "bad"}, "tgtg API error: state FAILURE: bad"},
	} {
		if got := tc.err.Error(); got != tc.want {
			t.Errorf("Error() = %q, want %q", got, tc.want)
		}
		if errors.Unwrap(tc.err) != nil {
			t.Errorf("error without a cause must unwrap to nil: %v", tc.err)
		}
	}
}

func TestCreateOrderRequiresOrder(t *testing.T) {
	for _, body := range []string{`{"state":"SUCCESS"}`, `{"state":"SUCCESS","order":null}`} {
		t.Run(body, func(t *testing.T) {
			m := newMockServer(t)
			m.addJSON(http.MethodPost, "/"+CreateOrderEndpoint+"item", http.StatusOK, json.RawMessage(body), nil)
			cfg := fakeTokensConfig()
			cfg.LastTimeTokenRefreshed = time.Now()
			c := newClient(t, m, cfg)
			order, err := c.CreateOrder(context.Background(), "item", 1)
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusOK || apiErr.Body != body+"\n" {
				t.Fatalf("expected APIError with status and body, got order=%v, error=%v", order, err)
			}
			if order != nil {
				t.Fatalf("invalid order returned: %v", order)
			}
		})
	}
}

func TestOrderFailuresKeepHTTPStatus(t *testing.T) {
	for _, method := range []string{"create", "abort"} {
		t.Run(method, func(t *testing.T) {
			m := newMockServer(t)
			path := "/" + CreateOrderEndpoint + "id"
			if method == "abort" {
				path = "/" + fmt.Sprintf(AbortOrderEndpoint, "id")
			}
			m.addJSON(http.MethodPost, path, http.StatusOK, map[string]string{"state": "FAILURE"}, nil)
			cfg := fakeTokensConfig()
			cfg.LastTimeTokenRefreshed = time.Now()
			c := newClient(t, m, cfg)
			var err error
			if method == "create" {
				_, err = c.CreateOrder(context.Background(), "id", 1)
			} else {
				err = c.AbortOrder(context.Background(), "id")
			}
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusOK || apiErr.State != "FAILURE" {
				t.Fatalf("expected APIError with HTTP 200 and FAILURE state, got %#v", err)
			}
		})
	}
}

func TestSignupRejectsIncompleteTokens(t *testing.T) {
	for _, body := range []string{
		`null`, `{}`, `{"login_response":null}`, `{"login_response":{}}`,
		`{"login_response":{"access_token":"new"}}`,
		`{"login_response":{"refresh_token":"new"}}`,
		`{"login_response":{"access_token":"","refresh_token":"new"}}`,
		`{"login_response":{"access_token":"new","refresh_token":null}}`,
	} {
		t.Run(body, func(t *testing.T) {
			m := newMockServer(t)
			m.addJSON(http.MethodPost, "/"+SignupByEmailEndpoint, http.StatusOK, json.RawMessage(body), nil)
			cfg := fakeTokensConfig()
			cfg.Cookie = "session=old"
			cfg.LastTimeTokenRefreshed = time.Now().Add(-time.Hour)
			c := newClient(t, m, cfg)
			cookie := c.Cookie
			err := c.SignupByEmail(context.Background(), DefaultSignupOptions("a@example.com"))
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusOK {
				t.Fatalf("expected APIError with HTTP 200, got %v", err)
			}
			if c.AccessToken != cfg.AccessToken || c.RefreshToken != cfg.RefreshToken || c.Cookie != cookie || !c.LastTimeTokenRefreshed.Equal(cfg.LastTimeTokenRefreshed) {
				t.Fatalf("invalid signup response changed credentials: access=%q refresh=%q cookie=%q refreshed=%v", c.AccessToken, c.RefreshToken, c.Cookie, c.LastTimeTokenRefreshed)
			}
		})
	}
}

func TestEndpointDecodeErrorsKeepCause(t *testing.T) {
	for _, method := range []string{"create", "abort", "signup"} {
		t.Run(method, func(t *testing.T) {
			m := newMockServer(t)
			path := "/" + CreateOrderEndpoint + "id"
			switch method {
			case "abort":
				path = "/" + fmt.Sprintf(AbortOrderEndpoint, "id")
			case "signup":
				path = "/" + SignupByEmailEndpoint
			}
			var calls int64
			m.routes = append(m.routes, &route{method: http.MethodPost, path: path, calls: &calls, handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"broken":`))
			}})
			cfg := fakeTokensConfig()
			cfg.LastTimeTokenRefreshed = time.Now()
			c := newClient(t, m, cfg)
			var err error
			switch method {
			case "create":
				_, err = c.CreateOrder(context.Background(), "id", 1)
			case "abort":
				err = c.AbortOrder(context.Background(), "id")
			case "signup":
				err = c.SignupByEmail(context.Background(), DefaultSignupOptions("a@example.com"))
			}
			var syntaxErr *json.SyntaxError
			if !errors.As(err, &syntaxErr) {
				t.Fatalf("expected wrapped JSON syntax error, got %v", err)
			}
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusOK || apiErr.Body != `{"broken":` {
				t.Fatalf("expected APIError with status and body, got %v", err)
			}
		})
	}
}
