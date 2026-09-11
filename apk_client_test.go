package tgtg

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type apkRoundTripFunc func(*http.Request) (*http.Response, error)

func (f apkRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestDefaultAPKFetcherUsesConfiguredHTTPClient(t *testing.T) {
	previousDefault := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = previousDefault })
	http.DefaultClient = &http.Client{Transport: apkRoundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Error("APK discovery used the default HTTP client")
		return nil, errors.New("unexpected default transport")
	})}
	requests := 0
	c := New(Config{
		Output: io.Discard,
		HTTPClient: &http.Client{Transport: apkRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			requests++
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(playStoreFixture)), Header: make(http.Header)}, nil
		})},
	})
	c.resolveUserAgent(context.Background())
	if requests != 1 || c.APKVersion != "26.5.0" {
		t.Fatalf("APK discovery made %d configured requests and selected %q; want one request and 26.5.0", requests, c.APKVersion)
	}
}

func TestCanceledAPKResolutionIsRetried(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requests := 0
	c := New(Config{
		Output: io.Discard,
		APKVersionFetcher: func(ctx context.Context) (string, error) {
			requests++
			if requests == 1 {
				cancel()
				return "", ctx.Err()
			}
			return "26.5.0", nil
		},
	})
	c.resolveUserAgent(ctx)
	if c.UserAgent != "" || c.APKVersion != "" {
		t.Fatalf("canceled discovery cached version %q and user agent %q", c.APKVersion, c.UserAgent)
	}
	c.resolveUserAgent(context.Background())
	if requests != 2 || c.APKVersion != "26.5.0" || !strings.Contains(c.UserAgent, "26.5.0") {
		t.Fatalf("retry made %d total requests and selected %q; want two requests and 26.5.0", requests, c.APKVersion)
	}
}
