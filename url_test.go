package tgtg

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestEndpointURLs(t *testing.T) {
	methods := []struct {
		name string
		path string
		call func(*Client, string) error
	}{
		{"item", APIItemEndpoint + "%s", func(c *Client, id string) error {
			_, err := c.GetItem(context.Background(), id)
			return err
		}},
		{"favorite", FavoriteItemEndpoint, func(c *Client, id string) error {
			return c.SetFavorite(context.Background(), id, true)
		}},
		{"create", CreateOrderEndpoint + "%s", func(c *Client, id string) error {
			_, err := c.CreateOrder(context.Background(), id, 1)
			return err
		}},
		{"status", OrderStatusEndpoint, func(c *Client, id string) error {
			_, err := c.GetOrderStatus(context.Background(), id)
			return err
		}},
		{"abort", AbortOrderEndpoint, func(c *Client, id string) error {
			return c.AbortOrder(context.Background(), id)
		}},
	}
	ids := []struct{ value, escaped string }{
		{"1234", "1234"},
		{"a?b#c", "a%3Fb%23c"},
		{"a/b", "a%2Fb"},
		{"a%2Fb", "a%252Fb"},
		{"a b", "a%20b"},
		{".", "%2E"},
		{"..", "%2E%2E"},
	}
	for _, basePath := range []string{"", "/", "/api", "/api/"} {
		for _, method := range methods {
			for _, id := range ids {
				t.Run(basePath+"/"+method.name+"/"+id.value, func(t *testing.T) {
					prefix := "/"
					if basePath == "/api" || basePath == "/api/" {
						prefix = "/api/"
					}
					want := prefix + fmt.Sprintf(method.path, id.escaped)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.RequestURI != want || r.Method != http.MethodPost {
							t.Errorf("request = %s %s, want POST %s", r.Method, r.RequestURI, want)
						}
						fmt.Fprint(w, `{"state":"SUCCESS","order":{}}`)
					}))
					defer server.Close()
					c := New(Config{
						URL:                    server.URL + basePath,
						AccessToken:            "access",
						RefreshToken:           "refresh",
						LastTimeTokenRefreshed: time.Now(),
						Cookie:                 "datadome=valid",
						UserAgent:              "test-agent",
						DataDomeSDKURL:         server.URL + "/sdk",
					})
					if err := method.call(c, id.value); err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
}
