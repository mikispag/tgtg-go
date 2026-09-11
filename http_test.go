package tgtg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestReadResponseBodyLimitsDecodedBytes(t *testing.T) {
	for _, encoding := range []string{"identity", "gzip"} {
		for _, size := range []int{8, 9} {
			payload := bytes.Repeat([]byte{'x'}, size)
			if encoding == "gzip" {
				payload = gzipBytes(t, payload)
			}
			resp := &http.Response{Header: http.Header{"Content-Encoding": {encoding}}, Body: io.NopCloser(bytes.NewReader(payload))}
			got, err := readResponseBody(resp, 8)
			resp.Body.Close()
			if size == 8 && (err != nil || len(got) != 8) || size == 9 && err == nil {
				t.Errorf("encoding=%s decoded=%d: len=%d err=%v", encoding, size, len(got), err)
			}
		}
	}
}

func TestAPIRejectsOversizedCompressedResponse(t *testing.T) {
	payload := gzipBytes(t, bytes.Repeat([]byte{' '}, int(maxAPIResponseBytes)+1))
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(payload)
	}))
	defer s.Close()
	c := New(Config{URL: s.URL + "/", UserAgent: "ua", Output: io.Discard})
	_, err := c.doPost(context.Background(), s.URL, nil)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized response accepted: %v", err)
	}
}

func TestDataDomeRejectsOversizedResponse(t *testing.T) {
	payload := gzipBytes(t, []byte(strings.Repeat(" ", int(maxDataDomeResponseBytes))+`{"status":200,"cookie":"datadome=fresh"}`))
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(payload)
	}))
	defer s.Close()
	c := New(Config{URL: s.URL + "/", DataDomeSDKURL: s.URL, UserAgent: "ua", Output: io.Discard})
	c.fetchDataDomeCookie(context.Background(), s.URL)
	if c.Cookie != "" {
		t.Fatalf("oversized SDK response stored a cookie: %q", c.Cookie)
	}
}

func TestEndpointDecodeErrorsPreserveMetadata(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		path string
		call func(*Client) error
	}{
		{"/" + APIItemEndpoint, func(c *Client) error { _, err := c.GetItems(ctx, DefaultGetItemsOptions()); return err }},
		{"/" + APIItemEndpoint + "123", func(c *Client) error { _, err := c.GetItem(ctx, "123"); return err }},
		{"/" + APIBucketEndpoint, func(c *Client) error { _, err := c.GetFavorites(ctx, DefaultGetFavoritesOptions()); return err }},
		{"/order/v8/123/status", func(c *Client) error { _, err := c.GetOrderStatus(ctx, "123"); return err }},
		{"/" + ActiveOrderEndpoint, func(c *Client) error { _, err := c.GetActive(ctx); return err }},
		{"/" + InactiveOrderEndpoint, func(c *Client) error { _, err := c.GetInactive(ctx, DefaultGetInactiveOptions()); return err }},
		{"/" + ManufacturerItemEndpoint, func(c *Client) error { _, err := c.GetManufacturerItems(ctx); return err }},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			m := newMockServer(t)
			m.addJSON(http.MethodPost, tc.path, http.StatusOK, "not an object", nil)
			cfg := fakeTokensConfig()
			cfg.LastTimeTokenRefreshed = time.Now()
			c := newClient(t, m, cfg)
			err := tc.call(c)
			var apiErr *APIError
			var cause *json.UnmarshalTypeError
			if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusOK || !errors.As(err, &cause) {
				t.Fatalf("decode failure lost metadata or cause: %v", err)
			}
		})
	}
}
