package tgtg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// route holds a registered handler for a (method, path) pair.
type route struct {
	method  string
	path    string
	handler http.HandlerFunc
	calls   *int64
}

// mockServer is a minimal substitute for Python's `responses` library.
type mockServer struct {
	t      *testing.T
	server *httptest.Server
	routes []*route
}

func newMockServer(t *testing.T) *mockServer {
	t.Helper()
	m := &mockServer{t: t}
	m.server = httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.server.Close)
	return m
}

func (m *mockServer) serve(w http.ResponseWriter, r *http.Request) {
	for _, rt := range m.routes {
		if rt.method == r.Method && rt.path == r.URL.Path {
			atomic.AddInt64(rt.calls, 1)
			rt.handler(w, r)
			return
		}
	}
	// Unmatched: 404 (used for DataDome URL etc.)
	w.WriteHeader(http.StatusNotFound)
}

func (m *mockServer) addJSON(method, path string, status int, body any, headers http.Header) *int64 {
	var counter int64
	rt := &route{
		method: method,
		path:   path,
		calls:  &counter,
		handler: func(w http.ResponseWriter, r *http.Request) {
			for k, vs := range headers {
				for _, v := range vs {
					w.Header().Add(k, v)
				}
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(body)
		},
	}
	m.routes = append(m.routes, rt)
	return &counter
}

func (m *mockServer) replaceJSON(method, path string, status int, body any, headers http.Header) *int64 {
	for i, rt := range m.routes {
		if rt.method == method && rt.path == path {
			m.routes = append(m.routes[:i], m.routes[i+1:]...)
			break
		}
	}
	return m.addJSON(method, path, status, body, headers)
}

// baseURL returns the mock server's URL with a trailing slash so it can stand
// in for the real BaseURL (which also ends with "/").
func (m *mockServer) baseURL() string { return m.server.URL + "/" }

// newClient returns a Client wired to the mock server with deterministic options.
func newClient(t *testing.T, m *mockServer, cfg Config) *Client {
	t.Helper()
	if cfg.URL == "" {
		cfg.URL = m.baseURL()
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = "test-agent"
	}
	if cfg.DataDomeSDKURL == "" {
		// Point DataDome at the mock server so it 404s instead of touching the real internet.
		cfg.DataDomeSDKURL = m.server.URL + "/datadome"
	}
	if cfg.Sleep == nil {
		cfg.Sleep = func(time.Duration) {}
	}
	if cfg.Output == nil {
		cfg.Output = io.Discard
	}
	return New(cfg)
}

func fakeTokensConfig() Config {
	return Config{
		AccessToken:  "access_token",
		RefreshToken: "refresh_token",
		Cookie:       "cookie",
	}
}

// helper that adds the standard refresh-tokens response.
func (m *mockServer) addRefreshTokensResponse() *int64 {
	return m.addJSON(http.MethodPost, "/"+RefreshEndpoint, http.StatusOK, map[string]any{
		"access_token":  "an_access_token",
		"refresh_token": "a_refresh_token",
	}, http.Header{"Set-Cookie": {"sweet sweet cookie"}})
}

func TestLoginWithTokens(t *testing.T) {
	m := newMockServer(t)
	m.addJSON(http.MethodPost, "/"+RefreshEndpoint, http.StatusOK,
		map[string]any{"access_token": "test", "refresh_token": "test_"},
		http.Header{"Set-Cookie": {"sweet sweet cookie"}})

	c := newClient(t, m, fakeTokensConfig())
	if err := c.Login(context.Background()); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if c.AccessToken != "test" || c.RefreshToken != "test_" || c.Cookie != "sweet sweet cookie" {
		t.Fatalf("unexpected credentials: %+v", c)
	}
}

func TestRefreshTokenAfterSomeTime(t *testing.T) {
	m := newMockServer(t)
	m.addRefreshTokensResponse()

	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	cur := base
	cfg := fakeTokensConfig()
	cfg.Now = func() time.Time { return cur }
	c := newClient(t, m, cfg)

	if err := c.Login(context.Background()); err != nil {
		t.Fatalf("first login: %v", err)
	}
	gotAccess, gotRefresh := c.AccessToken, c.RefreshToken

	m.replaceJSON(http.MethodPost, "/"+RefreshEndpoint, http.StatusOK, map[string]any{
		"access_token": "new_access_token", "refresh_token": "new_refresh_token",
	}, http.Header{"Set-Cookie": {"sweet sweet cookie"}})

	// Within lifetime: no refresh.
	cur = base.Add(DefaultAccessTokenLifetime)
	if err := c.Login(context.Background()); err != nil {
		t.Fatalf("login within lifetime: %v", err)
	}
	if c.AccessToken == "new_access_token" || c.RefreshToken == "new_refresh_token" {
		t.Fatalf("token refreshed unexpectedly within lifetime")
	}
	if c.AccessToken != gotAccess || c.RefreshToken != gotRefresh {
		t.Fatalf("token mutated within lifetime")
	}

	// Past lifetime: refresh.
	cur = base.Add(DefaultAccessTokenLifetime + time.Second)
	if err := c.Login(context.Background()); err != nil {
		t.Fatalf("login past lifetime: %v", err)
	}
	if c.AccessToken != "new_access_token" || c.RefreshToken != "new_refresh_token" {
		t.Fatalf("token not refreshed past lifetime: %+v", c)
	}
}

func TestRefreshTokenFail(t *testing.T) {
	m := newMockServer(t)
	m.addRefreshTokensResponse()

	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	cur := base
	cfg := fakeTokensConfig()
	cfg.Now = func() time.Time { return cur }
	c := newClient(t, m, cfg)

	if err := c.Login(context.Background()); err != nil {
		t.Fatalf("first login: %v", err)
	}
	oldAccess, oldRefresh := c.AccessToken, c.RefreshToken

	m.replaceJSON(http.MethodPost, "/"+RefreshEndpoint, http.StatusBadRequest, map[string]any{}, nil)
	cur = base.Add(DefaultAccessTokenLifetime + time.Second)

	err := c.Login(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected APIError, got %v", err)
	}
	if c.AccessToken != oldAccess || c.RefreshToken != oldRefresh {
		t.Fatalf("tokens mutated on failed refresh")
	}
}

func TestLoginEmptyFail(t *testing.T) {
	m := newMockServer(t)
	c := newClient(t, m, Config{})
	err := c.Login(context.Background())
	if err == nil {
		t.Fatalf("expected error from empty login")
	}
	if !strings.Contains(err.Error(), "email") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGetCredentials(t *testing.T) {
	m := newMockServer(t)
	m.addRefreshTokensResponse()
	c := newClient(t, m, fakeTokensConfig())

	creds, err := c.GetCredentials(context.Background())
	if err != nil {
		t.Fatalf("GetCredentials: %v", err)
	}
	want := Credentials{AccessToken: "an_access_token", RefreshToken: "a_refresh_token", Cookie: "sweet sweet cookie"}
	if creds != want {
		t.Fatalf("got %+v, want %+v", creds, want)
	}
}

func TestLoginWithTermsState(t *testing.T) {
	m := newMockServer(t)
	m.addJSON(http.MethodPost, "/"+AuthByEmailEndpoint, http.StatusOK, map[string]any{"state": "TERMS"}, nil)
	c := newClient(t, m, Config{Email: "newuser@test.com"})

	err := c.Login(context.Background())
	var pErr *PollingError
	if !errors.As(err, &pErr) {
		t.Fatalf("expected PollingError, got %v", err)
	}
	if !strings.Contains(pErr.Error(), "not linked to a tgtg account") {
		t.Fatalf("unexpected message: %v", pErr)
	}
}

func TestLoginWithUnknownState(t *testing.T) {
	m := newMockServer(t)
	m.addJSON(http.MethodPost, "/"+AuthByEmailEndpoint, http.StatusOK, map[string]any{"state": "UNKNOWN_STATE"}, nil)
	c := newClient(t, m, Config{Email: "test@test.com"})

	err := c.Login(context.Background())
	var lErr *LoginError
	if !errors.As(err, &lErr) {
		t.Fatalf("expected LoginError, got %v", err)
	}
	if lErr.StatusCode != 200 {
		t.Fatalf("got status %d, want 200", lErr.StatusCode)
	}
}

func TestLoginWithTooManyRequests(t *testing.T) {
	m := newMockServer(t)
	m.addJSON(http.MethodPost, "/"+AuthByEmailEndpoint, http.StatusTooManyRequests, map[string]any{}, nil)
	c := newClient(t, m, Config{Email: "test@test.com"})

	err := c.Login(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected APIError, got %v", err)
	}
	if !strings.Contains(apiErr.Body, "Too many requests") {
		t.Fatalf("unexpected body: %v", apiErr.Body)
	}
}

func TestGetItemsSuccess(t *testing.T) {
	m := newMockServer(t)
	m.addRefreshTokensResponse()
	calls := m.addJSON(http.MethodPost, "/"+APIItemEndpoint, http.StatusOK, map[string]any{"items": []any{}}, nil)
	c := newClient(t, m, fakeTokensConfig())

	items, err := c.GetItems(context.Background(), DefaultGetItemsOptions())
	if err != nil {
		t.Fatalf("GetItems: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected empty items, got %v", items)
	}
	if atomic.LoadInt64(calls) != 1 {
		t.Fatalf("expected 1 call to items endpoint, got %d", *calls)
	}
}

func TestGetItemsCustomUserAgent(t *testing.T) {
	m := newMockServer(t)
	m.addRefreshTokensResponse()
	const ua = "test"
	gotUA := make(chan string, 4)
	m.routes = append(m.routes, &route{
		method: http.MethodPost,
		path:   "/" + APIItemEndpoint,
		calls:  new(int64),
		handler: func(w http.ResponseWriter, r *http.Request) {
			select {
			case gotUA <- r.Header.Get("user-agent"):
			default:
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
		},
	})

	cfg := fakeTokensConfig()
	cfg.UserAgent = ua
	c := newClient(t, m, cfg)
	if _, err := c.GetItems(context.Background(), DefaultGetItemsOptions()); err != nil {
		t.Fatalf("GetItems: %v", err)
	}
	select {
	case got := <-gotUA:
		if got != ua {
			t.Fatalf("user agent: got %q, want %q", got, ua)
		}
	default:
		t.Fatalf("items endpoint not hit")
	}
}

func TestGetItemsFail(t *testing.T) {
	m := newMockServer(t)
	m.addRefreshTokensResponse()
	m.addJSON(http.MethodPost, "/"+APIItemEndpoint, http.StatusBadRequest, map[string]any{}, nil)
	c := newClient(t, m, fakeTokensConfig())

	_, err := c.GetItems(context.Background(), DefaultGetItemsOptions())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected APIError 400, got %v", err)
	}
}

func TestGetItemSuccess(t *testing.T) {
	m := newMockServer(t)
	m.addRefreshTokensResponse()
	m.addJSON(http.MethodPost, "/"+APIItemEndpoint+"1", http.StatusOK, map[string]any{}, nil)
	c := newClient(t, m, fakeTokensConfig())

	got, err := c.GetItem(context.Background(), "1")
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty map, got %v", got)
	}
}

func TestGetItemSuccessWithData(t *testing.T) {
	m := newMockServer(t)
	m.addRefreshTokensResponse()
	itemData := map[string]any{
		"item_id":         "123",
		"name":            "Test Item",
		"items_available": float64(5),
	}
	m.addJSON(http.MethodPost, "/item/v8/123", http.StatusOK, itemData, nil)
	c := newClient(t, m, fakeTokensConfig())

	got, err := c.GetItem(context.Background(), "123")
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if got["item_id"] != "123" || got["name"] != "Test Item" {
		t.Fatalf("unexpected payload: %v", got)
	}
}

func TestGetItemFail(t *testing.T) {
	m := newMockServer(t)
	m.addRefreshTokensResponse()
	m.addJSON(http.MethodPost, "/"+APIItemEndpoint+"1", http.StatusBadRequest, map[string]any{}, nil)
	c := newClient(t, m, fakeTokensConfig())

	_, err := c.GetItem(context.Background(), "1")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected APIError 400, got %v", err)
	}
}

func TestGetFavoritesParametrised(t *testing.T) {
	cases := []struct {
		name string
		body map[string]any
	}{
		{"empty body", map[string]any{}},
		{"empty bucket items", map[string]any{"mobile_bucket": map[string]any{"items": []any{}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newMockServer(t)
			m.addRefreshTokensResponse()
			m.addJSON(http.MethodPost, "/"+APIBucketEndpoint, http.StatusOK, tc.body, nil)
			c := newClient(t, m, fakeTokensConfig())

			got, err := c.GetFavorites(context.Background(), DefaultGetFavoritesOptions())
			if err != nil {
				t.Fatalf("GetFavorites: %v", err)
			}
			if len(got) != 0 {
				t.Fatalf("expected no favorites, got %v", got)
			}
		})
	}
}

func TestGetFavoritesWithItems(t *testing.T) {
	m := newMockServer(t)
	m.addRefreshTokensResponse()
	m.addJSON(http.MethodPost, "/"+APIBucketEndpoint, http.StatusOK, map[string]any{
		"mobile_bucket": map[string]any{"items": []map[string]any{
			{"item_id": "1", "name": "Item 1", "items_available": float64(3)},
			{"item_id": "2", "name": "Item 2", "items_available": float64(0)},
		}},
	}, nil)
	c := newClient(t, m, fakeTokensConfig())

	got, err := c.GetFavorites(context.Background(), DefaultGetFavoritesOptions())
	if err != nil {
		t.Fatalf("GetFavorites: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 items, got %d", len(got))
	}
	if got[0]["item_id"] != "1" {
		t.Fatalf("got %v", got[0])
	}
}

func TestGetFavoritesNonOK(t *testing.T) {
	m := newMockServer(t)
	m.addRefreshTokensResponse()
	m.addJSON(http.MethodPost, "/"+APIBucketEndpoint, http.StatusInternalServerError, map[string]any{}, nil)
	c := newClient(t, m, fakeTokensConfig())

	_, err := c.GetFavorites(context.Background(), DefaultGetFavoritesOptions())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %v", err)
	}
}

func TestSetFavoriteSuccess(t *testing.T) {
	m := newMockServer(t)
	m.addRefreshTokensResponse()
	calls := m.addJSON(http.MethodPost, "/"+fmt.Sprintf(FavoriteItemEndpoint, "item_123"), http.StatusOK, map[string]any{}, nil)
	c := newClient(t, m, fakeTokensConfig())

	if err := c.SetFavorite(context.Background(), "item_123", true); err != nil {
		t.Fatalf("SetFavorite: %v", err)
	}
	if atomic.LoadInt64(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", *calls)
	}
}

func TestSetFavoriteFail(t *testing.T) {
	m := newMockServer(t)
	m.addRefreshTokensResponse()
	m.addJSON(http.MethodPost, "/"+fmt.Sprintf(FavoriteItemEndpoint, "1"), http.StatusBadRequest, map[string]any{}, nil)
	c := newClient(t, m, fakeTokensConfig())

	err := c.SetFavorite(context.Background(), "1", true)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected APIError 400, got %v", err)
	}
}

func TestSetFavoriteNonOK(t *testing.T) {
	m := newMockServer(t)
	m.addRefreshTokensResponse()
	m.addJSON(http.MethodPost, "/"+fmt.Sprintf(FavoriteItemEndpoint, "item_123"), http.StatusInternalServerError, map[string]any{}, nil)
	c := newClient(t, m, fakeTokensConfig())

	err := c.SetFavorite(context.Background(), "item_123", true)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 500 {
		t.Fatalf("expected APIError 500, got %v", err)
	}
}

func TestGetManufacturerItemsSuccess(t *testing.T) {
	m := newMockServer(t)
	m.addRefreshTokensResponse()
	m.addJSON(http.MethodPost, "/"+ManufacturerItemEndpoint, http.StatusOK, map[string]any{}, nil)
	c := newClient(t, m, fakeTokensConfig())

	got, err := c.GetManufacturerItems(context.Background())
	if err != nil {
		t.Fatalf("GetManufacturerItems: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty map, got %v", got)
	}
}

func TestGetManufacturerItemsFail(t *testing.T) {
	m := newMockServer(t)
	m.addRefreshTokensResponse()
	m.addJSON(http.MethodPost, "/"+ManufacturerItemEndpoint, http.StatusBadRequest, map[string]any{}, nil)
	c := newClient(t, m, fakeTokensConfig())

	_, err := c.GetManufacturerItems(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected APIError 400, got %v", err)
	}
}

func TestCreateOrder(t *testing.T) {
	m := newMockServer(t)
	m.addRefreshTokensResponse()
	gotBody := make(chan map[string]any, 1)
	m.routes = append(m.routes, &route{
		method: http.MethodPost,
		path:   "/" + CreateOrderEndpoint + "1",
		calls:  new(int64),
		handler: func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			gotBody <- body
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"state": "SUCCESS", "order": map[string]any{}})
		},
	})
	c := newClient(t, m, fakeTokensConfig())

	order, err := c.CreateOrder(context.Background(), "1", 1)
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if len(order) != 0 {
		t.Fatalf("expected empty order, got %v", order)
	}
	body := <-gotBody
	if body["item_count"].(float64) != 1 {
		t.Fatalf("unexpected body: %v", body)
	}
}

func TestCreateOrderSuccessWithData(t *testing.T) {
	m := newMockServer(t)
	m.addRefreshTokensResponse()
	orderData := map[string]any{
		"order_id":     "order_123",
		"item_count":   float64(2),
		"total_amount": 25.50,
	}
	m.addJSON(http.MethodPost, "/"+CreateOrderEndpoint+"123", http.StatusOK,
		map[string]any{"state": "SUCCESS", "order": orderData}, nil)
	c := newClient(t, m, fakeTokensConfig())

	got, err := c.CreateOrder(context.Background(), "123", 2)
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if got["order_id"] != "order_123" {
		t.Fatalf("got %v", got)
	}
}

func TestCreateOrderFailureState(t *testing.T) {
	m := newMockServer(t)
	m.addRefreshTokensResponse()
	m.addJSON(http.MethodPost, "/"+CreateOrderEndpoint+"123", http.StatusOK,
		map[string]any{"state": "FAILURE", "message": "Item sold out"}, nil)
	c := newClient(t, m, fakeTokensConfig())

	_, err := c.CreateOrder(context.Background(), "123", 1)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.State != "FAILURE" {
		t.Fatalf("expected APIError state=FAILURE, got %v", err)
	}
}

func TestCreateOrderNonOK(t *testing.T) {
	m := newMockServer(t)
	m.addRefreshTokensResponse()
	m.addJSON(http.MethodPost, "/"+CreateOrderEndpoint+"123", http.StatusInternalServerError, map[string]any{}, nil)
	c := newClient(t, m, fakeTokensConfig())

	_, err := c.CreateOrder(context.Background(), "123", 1)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected APIError 500, got %v", err)
	}
}

func TestGetOrderStatusSuccess(t *testing.T) {
	m := newMockServer(t)
	m.addRefreshTokensResponse()
	data := map[string]any{"order_id": "order_123", "state": "COLLECTED", "item_count": float64(1)}
	m.addJSON(http.MethodPost, "/"+fmt.Sprintf(OrderStatusEndpoint, "order_123"), http.StatusOK, data, nil)
	c := newClient(t, m, fakeTokensConfig())

	got, err := c.GetOrderStatus(context.Background(), "order_123")
	if err != nil {
		t.Fatalf("GetOrderStatus: %v", err)
	}
	if got["state"] != "COLLECTED" {
		t.Fatalf("got %v", got)
	}
}

func TestGetOrderStatusFail(t *testing.T) {
	m := newMockServer(t)
	m.addRefreshTokensResponse()
	m.addJSON(http.MethodPost, "/"+fmt.Sprintf(OrderStatusEndpoint, "order_123"), http.StatusBadRequest, map[string]any{}, nil)
	c := newClient(t, m, fakeTokensConfig())

	_, err := c.GetOrderStatus(context.Background(), "order_123")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected APIError 400, got %v", err)
	}
}

func TestAbortOrderSuccess(t *testing.T) {
	m := newMockServer(t)
	m.addRefreshTokensResponse()
	m.addJSON(http.MethodPost, "/"+fmt.Sprintf(AbortOrderEndpoint, "order_123"), http.StatusOK,
		map[string]any{"state": "SUCCESS"}, nil)
	c := newClient(t, m, fakeTokensConfig())

	if err := c.AbortOrder(context.Background(), "order_123"); err != nil {
		t.Fatalf("AbortOrder: %v", err)
	}
}

func TestAbortOrderFail(t *testing.T) {
	m := newMockServer(t)
	m.addRefreshTokensResponse()
	m.addJSON(http.MethodPost, "/"+fmt.Sprintf(AbortOrderEndpoint, "order_123"), http.StatusOK,
		map[string]any{"state": "FAILURE", "message": "Cannot abort paid order"}, nil)
	c := newClient(t, m, fakeTokensConfig())

	err := c.AbortOrder(context.Background(), "order_123")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.State != "FAILURE" {
		t.Fatalf("expected APIError state=FAILURE, got %v", err)
	}
}

func TestGetActiveSuccess(t *testing.T) {
	m := newMockServer(t)
	m.addRefreshTokensResponse()
	m.addJSON(http.MethodPost, "/"+ActiveOrderEndpoint, http.StatusOK,
		map[string]any{"orders": []any{}}, http.Header{"Set-Cookie": {"session_id=12345; a=b; c=d"}})
	c := newClient(t, m, fakeTokensConfig())

	got, err := c.GetActive(context.Background())
	if err != nil {
		t.Fatalf("GetActive: %v", err)
	}
	orders, ok := got["orders"].([]any)
	if !ok || len(orders) != 0 {
		t.Fatalf("expected empty orders, got %v", got)
	}
}

func TestGetActiveFail(t *testing.T) {
	m := newMockServer(t)
	m.addRefreshTokensResponse()
	m.addJSON(http.MethodPost, "/"+ActiveOrderEndpoint, http.StatusBadRequest, map[string]any{}, nil)
	c := newClient(t, m, fakeTokensConfig())

	_, err := c.GetActive(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected APIError 400, got %v", err)
	}
}

func TestGetInactiveSuccess(t *testing.T) {
	m := newMockServer(t)
	m.addRefreshTokensResponse()
	m.addJSON(http.MethodPost, "/"+InactiveOrderEndpoint, http.StatusOK, map[string]any{"orders": []any{}}, nil)
	c := newClient(t, m, fakeTokensConfig())

	got, err := c.GetInactive(context.Background(), DefaultGetInactiveOptions())
	if err != nil {
		t.Fatalf("GetInactive: %v", err)
	}
	orders, ok := got["orders"].([]any)
	if !ok || len(orders) != 0 {
		t.Fatalf("expected empty orders, got %v", got)
	}
}

func TestGetInactiveFail(t *testing.T) {
	m := newMockServer(t)
	m.addRefreshTokensResponse()
	m.addJSON(http.MethodPost, "/"+InactiveOrderEndpoint, http.StatusBadRequest, map[string]any{}, nil)
	c := newClient(t, m, fakeTokensConfig())

	_, err := c.GetInactive(context.Background(), DefaultGetInactiveOptions())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected APIError 400, got %v", err)
	}
}

func TestGetInactiveWithPagination(t *testing.T) {
	m := newMockServer(t)
	m.addRefreshTokensResponse()
	m.addJSON(http.MethodPost, "/"+InactiveOrderEndpoint, http.StatusOK,
		map[string]any{"orders": []any{}, "paging": map[string]any{"page": float64(1), "size": float64(20)}}, nil)
	c := newClient(t, m, fakeTokensConfig())

	got, err := c.GetInactive(context.Background(), GetInactiveOptions{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("GetInactive: %v", err)
	}
	if _, ok := got["paging"]; !ok {
		t.Fatalf("expected paging in response, got %v", got)
	}
}

func TestSignupOK(t *testing.T) {
	m := newMockServer(t)
	m.addJSON(http.MethodPost, "/"+SignupByEmailEndpoint, http.StatusOK, map[string]any{
		"login_response": map[string]any{
			"access_token":  "an_access_token",
			"refresh_token": "a_refresh_token",
		},
	}, nil)
	c := newClient(t, m, Config{})

	if err := c.SignupByEmail(context.Background(), DefaultSignupOptions("test@test.com")); err != nil {
		t.Fatalf("SignupByEmail: %v", err)
	}
	if c.AccessToken != "an_access_token" || c.RefreshToken != "a_refresh_token" {
		t.Fatalf("unexpected tokens: %+v", c)
	}
}

func TestSignupFail(t *testing.T) {
	m := newMockServer(t)
	m.addJSON(http.MethodPost, "/"+SignupByEmailEndpoint, http.StatusBadRequest,
		map[string]any{"errors": []any{map[string]any{"code": "FAILED_SIGN_UP"}}}, nil)
	c := newClient(t, m, Config{})

	err := c.SignupByEmail(context.Background(), DefaultSignupOptions("test@test.com"))
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected APIError 400, got %v", err)
	}
}

func TestPollingSucceedsAfterPin(t *testing.T) {
	m := newMockServer(t)
	m.addJSON(http.MethodPost, "/"+AuthByEmailEndpoint, http.StatusOK,
		map[string]any{"state": "WAIT", "polling_id": "pid"}, nil)
	m.addJSON(http.MethodPost, "/"+AuthByRequestPinEndpoint, http.StatusOK,
		map[string]any{"access_token": "a", "refresh_token": "r"},
		http.Header{"Set-Cookie": {"yum"}})

	cfg := Config{
		Email:     "x@y.com",
		PinReader: func() (string, error) { return "1234", nil },
	}
	c := newClient(t, m, cfg)

	if err := c.Login(context.Background()); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if c.AccessToken != "a" || c.RefreshToken != "r" || c.Cookie != "yum" {
		t.Fatalf("unexpected post-login state: %+v", c)
	}
}

func TestPollingFallback(t *testing.T) {
	m := newMockServer(t)
	m.addJSON(http.MethodPost, "/"+AuthByEmailEndpoint, http.StatusOK,
		map[string]any{"state": "WAIT", "polling_id": "pid"}, nil)

	var attempts int64
	m.routes = append(m.routes, &route{
		method: http.MethodPost,
		path:   "/" + AuthPollingEndpoint,
		calls:  new(int64),
		handler: func(w http.ResponseWriter, r *http.Request) {
			n := atomic.AddInt64(&attempts, 1)
			if n < 2 {
				w.WriteHeader(http.StatusAccepted)
				return
			}
			w.Header().Set("Set-Cookie", "yum")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "a", "refresh_token": "r",
			})
		},
	})

	cfg := Config{
		Email:     "x@y.com",
		PinReader: func() (string, error) { return "", nil },
		Sleep:     func(time.Duration) {},
	}
	c := newClient(t, m, cfg)

	if err := c.Login(context.Background()); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if c.AccessToken != "a" {
		t.Fatalf("expected access token after polling, got %+v", c)
	}
}

// --- Login guard branches -------------------------------------------------

func TestLoginRequiresCookieWhenUsingTokens(t *testing.T) {
	m := newMockServer(t)
	c := newClient(t, m, Config{
		AccessToken:  "access",
		RefreshToken: "refresh",
		// Cookie missing on purpose.
	})
	err := c.Login(context.Background())
	if err == nil {
		t.Fatalf("expected error when cookie missing")
	}
	if !strings.Contains(err.Error(), "email") {
		t.Fatalf("error should reference what's missing: %v", err)
	}
}

func TestLoginAccessTokenOnlyFails(t *testing.T) {
	m := newMockServer(t)
	c := newClient(t, m, Config{AccessToken: "access"})
	if err := c.Login(context.Background()); err == nil {
		t.Fatalf("expected error with only access token set")
	}
}

func TestLoginRefreshTokenOnlyFails(t *testing.T) {
	m := newMockServer(t)
	c := newClient(t, m, Config{RefreshToken: "refresh"})
	if err := c.Login(context.Background()); err == nil {
		t.Fatalf("expected error with only refresh token set")
	}
}

// --- DataDome --------------------------------------------------------------

func TestDataDomeRetryOn403(t *testing.T) {
	m := newMockServer(t)
	m.addRefreshTokensResponse()

	var attempts int64
	m.routes = append(m.routes, &route{
		method: http.MethodPost,
		path:   "/" + APIItemEndpoint,
		calls:  new(int64),
		handler: func(w http.ResponseWriter, r *http.Request) {
			n := atomic.AddInt64(&attempts, 1)
			if n == 1 {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
		},
	})

	c := newClient(t, m, fakeTokensConfig())
	if _, err := c.GetItems(context.Background(), DefaultGetItemsOptions()); err != nil {
		t.Fatalf("GetItems: %v", err)
	}
	if got := atomic.LoadInt64(&attempts); got != 2 {
		t.Fatalf("expected 2 POSTs (one 403 + one retry), got %d", got)
	}
}

func TestDataDomeCookieInjected(t *testing.T) {
	m := newMockServer(t)

	// DataDome SDK handler returns a valid cookie.
	m.routes = append(m.routes, &route{
		method: http.MethodPost,
		path:   "/datadome",
		calls:  new(int64),
		handler: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": float64(200),
				"cookie": "datadome=COOKIE_VALUE; Max-Age=3600",
			})
		},
	})

	gotCookie := make(chan string, 4)
	m.routes = append(m.routes, &route{
		method: http.MethodPost,
		path:   "/" + RefreshEndpoint,
		calls:  new(int64),
		handler: func(w http.ResponseWriter, r *http.Request) {
			gotCookie <- r.Header.Get("Cookie")
			w.Header().Set("Set-Cookie", "session=ok")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "a", "refresh_token": "r",
			})
		},
	})

	c := newClient(t, m, fakeTokensConfig())
	if err := c.Login(context.Background()); err != nil {
		t.Fatalf("Login: %v", err)
	}

	cookieHeader := <-gotCookie
	if !strings.Contains(cookieHeader, "datadome=COOKIE_VALUE") {
		t.Fatalf("expected datadome cookie in request, got %q", cookieHeader)
	}
}

func TestDataDomeFetchSilentFailure(t *testing.T) {
	// DataDome returns junk JSON; the client must continue and not surface
	// the failure to the caller.
	m := newMockServer(t)
	m.routes = append(m.routes, &route{
		method: http.MethodPost,
		path:   "/datadome",
		calls:  new(int64),
		handler: func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("not json"))
		},
	})
	m.addRefreshTokensResponse()
	m.addJSON(http.MethodPost, "/"+ActiveOrderEndpoint, http.StatusOK,
		map[string]any{"orders": []any{}}, nil)

	c := newClient(t, m, fakeTokensConfig())
	if _, err := c.GetActive(context.Background()); err != nil {
		t.Fatalf("GetActive should succeed despite DataDome failure: %v", err)
	}
}

// --- Polling flow ----------------------------------------------------------

func TestPollingTooManyRequests(t *testing.T) {
	m := newMockServer(t)
	m.addJSON(http.MethodPost, "/"+AuthByEmailEndpoint, http.StatusOK,
		map[string]any{"state": "WAIT", "polling_id": "pid"}, nil)
	m.addJSON(http.MethodPost, "/"+AuthPollingEndpoint, http.StatusTooManyRequests,
		map[string]any{}, nil)

	cfg := Config{
		Email:     "x@y.com",
		PinReader: func() (string, error) { return "", nil },
		Sleep:     func(time.Duration) {},
	}
	c := newClient(t, m, cfg)

	err := c.Login(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected APIError, got %v", err)
	}
	if !strings.Contains(apiErr.Body, "Too many requests") {
		t.Fatalf("unexpected body: %v", apiErr.Body)
	}
}

func TestPollingUnknownStatus(t *testing.T) {
	m := newMockServer(t)
	m.addJSON(http.MethodPost, "/"+AuthByEmailEndpoint, http.StatusOK,
		map[string]any{"state": "WAIT", "polling_id": "pid"}, nil)
	m.addJSON(http.MethodPost, "/"+AuthPollingEndpoint, http.StatusInternalServerError,
		map[string]any{}, nil)

	cfg := Config{
		Email:     "x@y.com",
		PinReader: func() (string, error) { return "", nil },
		Sleep:     func(time.Duration) {},
	}
	c := newClient(t, m, cfg)

	err := c.Login(context.Background())
	var lErr *LoginError
	if !errors.As(err, &lErr) || lErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected LoginError 500, got %v", err)
	}
}

func TestPollingExhaustion(t *testing.T) {
	m := newMockServer(t)
	m.addJSON(http.MethodPost, "/"+AuthByEmailEndpoint, http.StatusOK,
		map[string]any{"state": "WAIT", "polling_id": "pid"}, nil)
	// Always 202 — never resolves.
	m.addJSON(http.MethodPost, "/"+AuthPollingEndpoint, http.StatusAccepted, map[string]any{}, nil)

	var slept int
	cfg := Config{
		Email:     "x@y.com",
		PinReader: func() (string, error) { return "", nil },
		Sleep:     func(d time.Duration) { slept++ },
	}
	c := newClient(t, m, cfg)

	err := c.Login(context.Background())
	var pErr *PollingError
	if !errors.As(err, &pErr) {
		t.Fatalf("expected PollingError, got %v", err)
	}
	if !strings.Contains(pErr.Error(), "Max retries") {
		t.Fatalf("unexpected message: %v", pErr)
	}
	if slept != MaxPollingTries {
		t.Fatalf("expected %d sleeps, got %d", MaxPollingTries, slept)
	}
}

func TestPinReaderError(t *testing.T) {
	m := newMockServer(t)
	m.addJSON(http.MethodPost, "/"+AuthByEmailEndpoint, http.StatusOK,
		map[string]any{"state": "WAIT", "polling_id": "pid"}, nil)

	wantErr := errors.New("stdin broken")
	cfg := Config{
		Email:     "x@y.com",
		PinReader: func() (string, error) { return "", wantErr },
	}
	c := newClient(t, m, cfg)

	err := c.Login(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected %v, got %v", wantErr, err)
	}
}

func TestPinAuthBadStatus(t *testing.T) {
	m := newMockServer(t)
	m.addJSON(http.MethodPost, "/"+AuthByEmailEndpoint, http.StatusOK,
		map[string]any{"state": "WAIT", "polling_id": "pid"}, nil)
	m.addJSON(http.MethodPost, "/"+AuthByRequestPinEndpoint, http.StatusBadRequest,
		map[string]any{}, nil)

	cfg := Config{
		Email:     "x@y.com",
		PinReader: func() (string, error) { return "0000", nil },
	}
	c := newClient(t, m, cfg)

	err := c.Login(context.Background())
	var lErr *LoginError
	if !errors.As(err, &lErr) || lErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected LoginError 400, got %v", err)
	}
}

// --- Headers ---------------------------------------------------------------

func TestRequestHeadersPropagated(t *testing.T) {
	m := newMockServer(t)
	m.addRefreshTokensResponse()

	gotHeaders := make(chan http.Header, 4)
	m.routes = append(m.routes, &route{
		method: http.MethodPost,
		path:   "/" + ActiveOrderEndpoint,
		calls:  new(int64),
		handler: func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Clone()
			gotHeaders <- h
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"orders": []any{}})
		},
	})

	cfg := fakeTokensConfig()
	cfg.Language = "fr-CH"
	c := newClient(t, m, cfg)
	if _, err := c.GetActive(context.Background()); err != nil {
		t.Fatalf("GetActive: %v", err)
	}
	h := <-gotHeaders
	if got := h.Get("Authorization"); got != "Bearer "+c.AccessToken {
		t.Fatalf("Authorization: got %q, want Bearer %s", got, c.AccessToken)
	}
	if got := h.Get("Cookie"); got != c.Cookie {
		t.Fatalf("Cookie: got %q, want %q", got, c.Cookie)
	}
	if got := h.Get("Accept-Language"); got != "fr-CH" {
		t.Fatalf("Accept-Language: got %q, want fr-CH", got)
	}
	if got := h.Get("X-Correlation-ID"); got == "" {
		t.Fatalf("X-Correlation-ID should be set")
	}
	if got := h.Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("Content-Type: got %q", got)
	}
}

// --- Multi-Set-Cookie collapse --------------------------------------------

func TestRefreshCollapsesMultipleSetCookies(t *testing.T) {
	m := newMockServer(t)
	m.routes = append(m.routes, &route{
		method: http.MethodPost,
		path:   "/" + RefreshEndpoint,
		calls:  new(int64),
		handler: func(w http.ResponseWriter, r *http.Request) {
			// Send TWO Set-Cookie headers; the client must capture both.
			w.Header().Add("Set-Cookie", "session=abc; Path=/")
			w.Header().Add("Set-Cookie", "datadome=ddv; Path=/; Secure")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "a", "refresh_token": "r",
			})
		},
	})

	c := newClient(t, m, fakeTokensConfig())
	if err := c.Login(context.Background()); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if !strings.Contains(c.Cookie, "session=abc") {
		t.Fatalf("expected session cookie in c.Cookie, got %q", c.Cookie)
	}
	if !strings.Contains(c.Cookie, "datadome=ddv") {
		t.Fatalf("expected datadome cookie in c.Cookie, got %q", c.Cookie)
	}
}

func TestSignupStoresCookie(t *testing.T) {
	m := newMockServer(t)
	m.addJSON(http.MethodPost, "/"+SignupByEmailEndpoint, http.StatusOK, map[string]any{
		"login_response": map[string]any{
			"access_token": "a", "refresh_token": "r",
		},
	}, http.Header{"Set-Cookie": {"session=signup-cookie"}})
	c := newClient(t, m, Config{})

	if err := c.SignupByEmail(context.Background(), DefaultSignupOptions("test@test.com")); err != nil {
		t.Fatalf("SignupByEmail: %v", err)
	}
	if c.Cookie != "session=signup-cookie" {
		t.Fatalf("cookie not stored on signup, got %q", c.Cookie)
	}
}

// --- Malformed JSON paths --------------------------------------------------

func TestRefreshTokenMalformedJSON(t *testing.T) {
	m := newMockServer(t)
	m.routes = append(m.routes, &route{
		method: http.MethodPost,
		path:   "/" + RefreshEndpoint,
		calls:  new(int64),
		handler: func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("not json"))
		},
	})
	c := newClient(t, m, fakeTokensConfig())

	err := c.Login(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected APIError on malformed JSON, got %v", err)
	}
}

func TestAuthByEmailMalformedJSON(t *testing.T) {
	m := newMockServer(t)
	m.routes = append(m.routes, &route{
		method: http.MethodPost,
		path:   "/" + AuthByEmailEndpoint,
		calls:  new(int64),
		handler: func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("garbage"))
		},
	})
	c := newClient(t, m, Config{Email: "x@y.com"})

	err := c.Login(context.Background())
	var lErr *LoginError
	if !errors.As(err, &lErr) {
		t.Fatalf("expected LoginError on malformed JSON, got %v", err)
	}
}

func TestSignupMalformedJSON(t *testing.T) {
	m := newMockServer(t)
	m.routes = append(m.routes, &route{
		method: http.MethodPost,
		path:   "/" + SignupByEmailEndpoint,
		calls:  new(int64),
		handler: func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("not json"))
		},
	})
	c := newClient(t, m, Config{})
	err := c.SignupByEmail(context.Background(), DefaultSignupOptions("a@b.com"))
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected APIError on malformed JSON, got %v", err)
	}
}

// --- Request body shapes ---------------------------------------------------

func TestSetFavoriteBody(t *testing.T) {
	m := newMockServer(t)
	m.addRefreshTokensResponse()

	gotBody := make(chan map[string]any, 1)
	m.routes = append(m.routes, &route{
		method: http.MethodPost,
		path:   "/" + fmt.Sprintf(FavoriteItemEndpoint, "1"),
		calls:  new(int64),
		handler: func(w http.ResponseWriter, r *http.Request) {
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			gotBody <- b
			w.WriteHeader(http.StatusOK)
		},
	})
	c := newClient(t, m, fakeTokensConfig())
	if err := c.SetFavorite(context.Background(), "1", true); err != nil {
		t.Fatalf("SetFavorite: %v", err)
	}
	body := <-gotBody
	if body["is_favorite"] != true {
		t.Fatalf("is_favorite: got %v", body["is_favorite"])
	}
}

func TestAbortOrderBodyHasCancelReasonID(t *testing.T) {
	m := newMockServer(t)
	m.addRefreshTokensResponse()

	gotBody := make(chan map[string]any, 1)
	m.routes = append(m.routes, &route{
		method: http.MethodPost,
		path:   "/" + fmt.Sprintf(AbortOrderEndpoint, "o1"),
		calls:  new(int64),
		handler: func(w http.ResponseWriter, r *http.Request) {
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			gotBody <- b
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"state": "SUCCESS"})
		},
	})
	c := newClient(t, m, fakeTokensConfig())
	if err := c.AbortOrder(context.Background(), "o1"); err != nil {
		t.Fatalf("AbortOrder: %v", err)
	}
	body := <-gotBody
	if body["cancel_reason_id"].(float64) != 1 {
		t.Fatalf("cancel_reason_id: got %v, want 1", body["cancel_reason_id"])
	}
}

func TestSignupBodyDefaultsCountryGB(t *testing.T) {
	m := newMockServer(t)

	gotBody := make(chan map[string]any, 1)
	m.routes = append(m.routes, &route{
		method: http.MethodPost,
		path:   "/" + SignupByEmailEndpoint,
		calls:  new(int64),
		handler: func(w http.ResponseWriter, r *http.Request) {
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			gotBody <- b
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"login_response": map[string]any{"access_token": "a", "refresh_token": "r"},
			})
		},
	})
	c := newClient(t, m, Config{})
	// Pass empty CountryID — the method should default it to "GB".
	if err := c.SignupByEmail(context.Background(), SignupOptions{Email: "a@b.com"}); err != nil {
		t.Fatalf("SignupByEmail: %v", err)
	}
	body := <-gotBody
	if body["country_id"] != "GB" {
		t.Fatalf("country_id default: got %v, want GB", body["country_id"])
	}
}

func TestGetItemsBodyWithAllFields(t *testing.T) {
	m := newMockServer(t)
	m.addRefreshTokensResponse()

	gotBody := make(chan map[string]any, 1)
	m.routes = append(m.routes, &route{
		method: http.MethodPost,
		path:   "/" + APIItemEndpoint,
		calls:  new(int64),
		handler: func(w http.ResponseWriter, r *http.Request) {
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			gotBody <- b
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
		},
	})

	earliest := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	latest := earliest.Add(2 * time.Hour)

	c := newClient(t, m, fakeTokensConfig())
	opts := DefaultGetItemsOptions()
	opts.FavoritesOnly = false
	opts.Latitude = 47.37
	opts.Longitude = 8.54
	opts.Radius = 5
	opts.PageSize = 10
	opts.Page = 2
	opts.Discover = true
	opts.ItemCategories = []string{"BAKED_GOODS"}
	opts.DietCategories = []string{"VEGETARIAN"}
	opts.PickupEarliest = &earliest
	opts.PickupLatest = &latest
	opts.SearchPhrase = "bakery"
	opts.WithStockOnly = true
	opts.HiddenOnly = true
	opts.WeCareOnly = true

	if _, err := c.GetItems(context.Background(), opts); err != nil {
		t.Fatalf("GetItems: %v", err)
	}
	body := <-gotBody

	checks := map[string]any{
		"favorites_only":  false,
		"radius":          float64(5),
		"page_size":       float64(10),
		"page":            float64(2),
		"discover":        true,
		"with_stock_only": true,
		"hidden_only":     true,
		"we_care_only":    true,
		"search_phrase":   "bakery",
	}
	for k, want := range checks {
		if body[k] != want {
			t.Errorf("%s: got %v, want %v", k, body[k], want)
		}
	}
	origin := body["origin"].(map[string]any)
	if origin["latitude"].(float64) != 47.37 || origin["longitude"].(float64) != 8.54 {
		t.Errorf("origin: got %v", origin)
	}
	if !strings.HasPrefix(body["pickup_earliest"].(string), "2026-05-06") {
		t.Errorf("pickup_earliest: got %v", body["pickup_earliest"])
	}
	if !strings.HasPrefix(body["pickup_latest"].(string), "2026-05-06") {
		t.Errorf("pickup_latest: got %v", body["pickup_latest"])
	}
}

func TestGetManufacturerItemsBody(t *testing.T) {
	m := newMockServer(t)
	m.addRefreshTokensResponse()

	gotBody := make(chan map[string]any, 1)
	m.routes = append(m.routes, &route{
		method: http.MethodPost,
		path:   "/" + ManufacturerItemEndpoint,
		calls:  new(int64),
		handler: func(w http.ResponseWriter, r *http.Request) {
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			gotBody <- b
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{})
		},
	})
	c := newClient(t, m, fakeTokensConfig())
	if _, err := c.GetManufacturerItems(context.Background()); err != nil {
		t.Fatalf("GetManufacturerItems: %v", err)
	}
	body := <-gotBody
	display := body["display_types_accepted"].([]any)
	if len(display) != 1 || display[0] != "LIST" {
		t.Errorf("display_types_accepted: got %v", display)
	}
	elements := body["element_types_accepted"].([]any)
	if len(elements) != 5 {
		t.Errorf("element_types_accepted should have 5 entries, got %d", len(elements))
	}
}

// --- Errors ----------------------------------------------------------------

func TestErrorTypesFormatting(t *testing.T) {
	cases := []struct {
		err      error
		contains string
	}{
		{&LoginError{StatusCode: 401, Body: "bad"}, "401"},
		{&APIError{StatusCode: 500, Body: "boom"}, "500"},
		{&APIError{State: "FAILURE", Body: "sold out"}, "FAILURE"},
		{&PollingError{Message: "timeout"}, "timeout"},
	}
	for _, tc := range cases {
		if !strings.Contains(tc.err.Error(), tc.contains) {
			t.Errorf("%T.Error()=%q should contain %q", tc.err, tc.err.Error(), tc.contains)
		}
	}
}

// --- Context cancellation --------------------------------------------------

func TestContextCancellationPropagates(t *testing.T) {
	m := newMockServer(t)
	m.addRefreshTokensResponse()
	m.addJSON(http.MethodPost, "/"+ActiveOrderEndpoint, http.StatusOK,
		map[string]any{"orders": []any{}}, nil)

	c := newClient(t, m, fakeTokensConfig())
	// Pre-cancel: every HTTP call inside the chain (DataDome, refresh, active)
	// must propagate the cancellation back to the caller.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.GetActive(ctx)
	if err == nil {
		t.Fatalf("expected error from cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// --- Constructor defaults --------------------------------------------------

func TestNewAppliesDefaults(t *testing.T) {
	c := New(Config{
		AccessToken:  "a",
		RefreshToken: "r",
		Cookie:       "c",
		UserAgent:    "ua",
		Output:       io.Discard,
	})
	if c.BaseURL != BaseURL {
		t.Errorf("BaseURL default: got %q", c.BaseURL)
	}
	if c.Language != "en-GB" {
		t.Errorf("Language default: got %q", c.Language)
	}
	if c.DeviceType != "ANDROID" {
		t.Errorf("DeviceType default: got %q", c.DeviceType)
	}
	if c.AccessTokenLifetime != DefaultAccessTokenLifetime {
		t.Errorf("AccessTokenLifetime default: got %v", c.AccessTokenLifetime)
	}
	if c.dataDomeSDKURL != DataDomeSDKURL {
		t.Errorf("dataDomeSDKURL default: got %q", c.dataDomeSDKURL)
	}
	if c.now == nil || c.sleep == nil || c.pinReader == nil || c.out == nil {
		t.Errorf("function defaults must be wired: now=%v sleep=%v pin=%v out=%v",
			c.now != nil, c.sleep != nil, c.pinReader != nil, c.out != nil)
	}
	if c.correlationID == "" {
		t.Errorf("correlationID should be generated")
	}
}

func TestNewBuildsUserAgentEagerlyWhenAPKVersionGiven(t *testing.T) {
	c := New(Config{
		AccessToken:  "a",
		RefreshToken: "r",
		Cookie:       "c",
		APKVersion:   "26.5.0",
		Output:       io.Discard,
	})
	if !strings.Contains(c.UserAgent, "26.5.0") {
		t.Fatalf("UserAgent should embed APKVersion, got %q", c.UserAgent)
	}
}

// --- collectSetCookie ------------------------------------------------------

func TestCollectSetCookie(t *testing.T) {
	cases := []struct {
		name string
		h    http.Header
		want string
	}{
		{"empty", http.Header{}, ""},
		{"single", http.Header{"Set-Cookie": {"a=1"}}, "a=1"},
		{"multi", http.Header{"Set-Cookie": {"a=1", "b=2; Path=/"}}, "a=1, b=2; Path=/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := collectSetCookie(tc.h); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// --- Custom HTTPClient -----------------------------------------------------

func TestCustomHTTPClientPropagates(t *testing.T) {
	custom := &http.Client{Timeout: 13 * time.Second}
	c := New(Config{
		AccessToken:  "a",
		RefreshToken: "r",
		Cookie:       "c",
		UserAgent:    "ua",
		HTTPClient:   custom,
		Output:       io.Discard,
	})
	if c.httpClient != custom {
		t.Fatalf("custom HTTPClient should be used")
	}
	if c.httpClient.Jar == nil {
		t.Fatalf("Jar must be populated even on injected client")
	}
}

// --- GetItems URL construction ---------------------------------------------

func TestGetItemURLContainsItemID(t *testing.T) {
	m := newMockServer(t)
	m.addRefreshTokensResponse()
	gotPath := make(chan string, 1)
	m.routes = append(m.routes, &route{
		method: http.MethodPost,
		path:   "/item/v8/abc-123",
		calls:  new(int64),
		handler: func(w http.ResponseWriter, r *http.Request) {
			gotPath <- r.URL.Path
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{})
		},
	})
	c := newClient(t, m, fakeTokensConfig())
	if _, err := c.GetItem(context.Background(), "abc-123"); err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if got := <-gotPath; got != "/item/v8/abc-123" {
		t.Fatalf("path: got %q", got)
	}
}

func TestCreateOrderURLContainsItemID(t *testing.T) {
	m := newMockServer(t)
	m.addRefreshTokensResponse()
	gotPath := make(chan string, 1)
	m.routes = append(m.routes, &route{
		method: http.MethodPost,
		path:   "/order/v8/create/xyz",
		calls:  new(int64),
		handler: func(w http.ResponseWriter, r *http.Request) {
			gotPath <- r.URL.Path
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"state": "SUCCESS", "order": map[string]any{}})
		},
	})
	c := newClient(t, m, fakeTokensConfig())
	if _, err := c.CreateOrder(context.Background(), "xyz", 1); err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if got := <-gotPath; got != "/order/v8/create/xyz" {
		t.Fatalf("path: got %q", got)
	}
}

// --- newUUID ---------------------------------------------------------------

func TestNewUUIDShape(t *testing.T) {
	u := newUUID()
	// 8-4-4-4-12 hex
	parts := strings.Split(u, "-")
	if len(parts) != 5 {
		t.Fatalf("UUID parts: got %d, want 5 (%q)", len(parts), u)
	}
	wantLengths := []int{8, 4, 4, 4, 12}
	for i, p := range parts {
		if len(p) != wantLengths[i] {
			t.Errorf("part %d length: got %d, want %d", i, len(p), wantLengths[i])
		}
	}
	// Version 4 → first hex char of part[2] is '4'.
	if parts[2][0] != '4' {
		t.Errorf("UUID v4 marker: got %q", parts[2])
	}
}

func TestNewUUIDUnique(t *testing.T) {
	a := newUUID()
	b := newUUID()
	if a == b {
		t.Fatalf("UUIDs collided: %q == %q", a, b)
	}
}

// --- generateDataDomeCID ---------------------------------------------------

func TestGenerateDataDomeCID(t *testing.T) {
	cid := generateDataDomeCID()
	if len(cid) != 120 {
		t.Fatalf("CID length: got %d, want 120", len(cid))
	}
	allowed := dataDomeCIDChars
	for _, r := range cid {
		if !strings.ContainsRune(allowed, r) {
			t.Fatalf("CID contains disallowed rune %q", r)
		}
	}
}

// Sanity check: the request body for GetItems matches the documented shape.
func TestGetItemsRequestBody(t *testing.T) {
	m := newMockServer(t)
	m.addRefreshTokensResponse()

	gotBody := make(chan map[string]any, 1)
	m.routes = append(m.routes, &route{
		method: http.MethodPost,
		path:   "/" + APIItemEndpoint,
		calls:  new(int64),
		handler: func(w http.ResponseWriter, r *http.Request) {
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			gotBody <- b
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
		},
	})

	c := newClient(t, m, fakeTokensConfig())
	if _, err := c.GetItems(context.Background(), DefaultGetItemsOptions()); err != nil {
		t.Fatalf("GetItems: %v", err)
	}
	body := <-gotBody
	if body["favorites_only"] != true {
		t.Fatalf("favorites_only default should be true, got %v", body["favorites_only"])
	}
	if body["page_size"].(float64) != 20 {
		t.Fatalf("page_size default should be 20, got %v", body["page_size"])
	}
}

// --- Lazy User-Agent resolution -------------------------------------------

func TestNewDefersUserAgentWhenNoAPKVersionGiven(t *testing.T) {
	c := New(Config{
		AccessToken:  "a",
		RefreshToken: "r",
		Cookie:       "c",
		Output:       io.Discard,
	})
	if c.UserAgent != "" {
		t.Fatalf("UserAgent should be empty until first request, got %q", c.UserAgent)
	}
}

func TestPostLazilyResolvesUserAgent(t *testing.T) {
	m := newMockServer(t)
	m.addRefreshTokensResponse()
	gotUA := make(chan string, 1)
	m.routes = append(m.routes, &route{
		method: http.MethodPost,
		path:   "/" + APIItemEndpoint,
		calls:  new(int64),
		handler: func(w http.ResponseWriter, r *http.Request) {
			gotUA <- r.Header.Get("User-Agent")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
		},
	})

	c := New(Config{
		AccessToken:    "access_token",
		RefreshToken:   "refresh_token",
		Cookie:         "cookie",
		URL:            m.baseURL(),
		DataDomeSDKURL: m.server.URL + "/datadome",
		Output:         io.Discard,
		Sleep:          func(time.Duration) {},
		APKVersionFetcher: func(context.Context) (string, error) {
			return "99.88.77", nil
		},
	})
	if c.UserAgent != "" {
		t.Fatalf("UserAgent should defer resolution, got %q", c.UserAgent)
	}
	if _, err := c.GetItems(context.Background(), DefaultGetItemsOptions()); err != nil {
		t.Fatalf("GetItems: %v", err)
	}
	got := <-gotUA
	if !strings.Contains(got, "99.88.77") {
		t.Fatalf("UserAgent header should embed fetched version, got %q", got)
	}
	if !strings.Contains(c.UserAgent, "99.88.77") {
		t.Fatalf("c.UserAgent should be populated after first request, got %q", c.UserAgent)
	}
}

func TestPostUsesRequestContextForAPKFetch(t *testing.T) {
	m := newMockServer(t)
	m.addRefreshTokensResponse()

	type ctxKey struct{}
	seen := make(chan context.Context, 1)
	c := New(Config{
		AccessToken:    "access_token",
		RefreshToken:   "refresh_token",
		Cookie:         "cookie",
		URL:            m.baseURL(),
		DataDomeSDKURL: m.server.URL + "/datadome",
		Output:         io.Discard,
		Sleep:          func(time.Duration) {},
		APKVersionFetcher: func(ctx context.Context) (string, error) {
			seen <- ctx
			return DefaultAPKVersion, nil
		},
	})
	ctx := context.WithValue(context.Background(), ctxKey{}, "marker")
	if err := c.Login(ctx); err != nil {
		t.Fatalf("Login: %v", err)
	}
	select {
	case got := <-seen:
		if got.Value(ctxKey{}) != "marker" {
			t.Fatalf("APK fetcher should receive request ctx; got value %v", got.Value(ctxKey{}))
		}
	default:
		t.Fatalf("APK fetcher was not invoked")
	}
}

func TestPostFallsBackToDefaultAPKVersionOnFetchError(t *testing.T) {
	m := newMockServer(t)
	m.addRefreshTokensResponse()

	c := New(Config{
		AccessToken:    "access_token",
		RefreshToken:   "refresh_token",
		Cookie:         "cookie",
		URL:            m.baseURL(),
		DataDomeSDKURL: m.server.URL + "/datadome",
		Output:         io.Discard,
		Sleep:          func(time.Duration) {},
		APKVersionFetcher: func(context.Context) (string, error) {
			return "", errors.New("offline")
		},
	})
	if err := c.Login(context.Background()); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if !strings.Contains(c.UserAgent, DefaultAPKVersion) {
		t.Fatalf("expected fallback to DefaultAPKVersion, got %q", c.UserAgent)
	}
}

// --- stdin PIN reader writer routing --------------------------------------

func TestStdinPinReaderUsesProvidedWriter(t *testing.T) {
	var out strings.Builder
	in := strings.NewReader("4321\n")
	pin, err := stdinPinReader(&out, in)
	if err != nil {
		t.Fatalf("stdinPinReader: %v", err)
	}
	if !strings.Contains(out.String(), "Enter PIN from email") {
		t.Fatalf("prompt should be written to provided writer, got %q", out.String())
	}
	if strings.TrimSpace(pin) != "4321" {
		t.Fatalf("pin: got %q, want 4321", pin)
	}
}
