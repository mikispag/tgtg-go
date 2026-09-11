package tgtg

import (
	"context"
	"net/http"
	"testing"
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
