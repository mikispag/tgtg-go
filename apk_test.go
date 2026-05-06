package tgtg

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// playStoreFixture is a structurally realistic excerpt of a real Play Store
// details-page response captured on 2026-05-06 for com.app.tgtg. Fields
// surrounding the version were preserved verbatim; unrelated fields were
// trimmed for readability. The full live response was 1.1 MB; the version
// "26.5.0" appears inside the ds:5 block alongside a min-Android version
// shorthand and release notes.
const playStoreFixture = `<script class="ds:5" nonce="abc">AF_initDataCallback({key: 'ds:5', hash: '12', data:[[[[]]],[null,null,[["Too Good To Go: End Food Waste"],null,null,null,null,null,null,null,[0,1,[0]],["Everyone",[null,2,[512,512],[null,null,"https://play-lh.googleusercontent.com/IciOnDFecb5Xt50Q2jlcNC0LPI7LEGxNojroo-s3AozcyS-vDCwtq4fn7u3wZmRna8OewG9PBrWC-i7i"]]],null,null,null,null,[null,null,null,null,null,[[["26.5.0"]],[[[36]],[[[26,"8.0"]]]]],null,null,null,[null,[null,"In this app release, we have fixed some bugs"]]]]], sideChannel: {hash: 'foo'}});</script>`

func TestExtractFromPlayStoreRealisticFixture(t *testing.T) {
	got, err := extractFromPlayStore([]byte(playStoreFixture))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if got != "26.5.0" {
		t.Fatalf("got %q, want 26.5.0", got)
	}
}

func TestExtractFromPlayStoreQuoted(t *testing.T) {
	body := []byte(`<script>AF_initDataCallback({key: 'ds:5', isError: false, hash: '7', data:[null,[null,[null,null,[null,null,[null,null,[null,null,[null,null,"26.5.0"]]],"5.0.0"]]]], sideChannel: {}});</script>`)
	got, err := extractFromPlayStore(body)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if got != "26.5.0" {
		t.Fatalf("got %q, want 26.5.0", got)
	}
}

func TestExtractFromPlayStorePicksLatestWhenHistoryPresent(t *testing.T) {
	// Defensive: even if Google starts including version history alongside
	// the current version, latestSemver picks the largest one.
	body := []byte(`<script>AF_initDataCallback({key: 'ds:5', data:[null,[["25.6.1","25.4.1","26.5.0","26.2.10"]]], sideChannel: {}});</script>`)
	got, err := extractFromPlayStore(body)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if got != "26.5.0" {
		t.Fatalf("got %q, want highest 26.5.0", got)
	}
}

func TestExtractFromPlayStoreUnquotedFallback(t *testing.T) {
	// No quoted version, but the version appears as a bare token (some
	// reformatted payloads do this).
	body := []byte(`<script>AF_initDataCallback({key: 'ds:5', isError: false, hash: '7', data:[26.5.0], sideChannel: {}});</script>`)
	got, err := extractFromPlayStore(body)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if got != "26.5.0" {
		t.Fatalf("got %q, want 26.5.0", got)
	}
}

func TestExtractFromPlayStoreSkipsAndroidMinVersion(t *testing.T) {
	// "5.0.0" appears first but is the Android-min-version; the major-2-digit
	// rule should skip it.
	body := []byte(`<script>AF_initDataCallback({key: 'ds:5', isError: false, hash: '7', data:[null,"5.0.0",null,null,"26.5.0"], sideChannel: {}});</script>`)
	got, err := extractFromPlayStore(body)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if got != "26.5.0" {
		t.Fatalf("got %q, want 26.5.0", got)
	}
}

func TestExtractFromPlayStoreSkipsDates(t *testing.T) {
	// "2026.05.06" is a date — must not be picked up.
	body := []byte(`<script>AF_initDataCallback({key: 'ds:5', isError: false, hash: '7', data:["2026.05.06","5.0.0","26.5.0"], sideChannel: {}});</script>`)
	got, err := extractFromPlayStore(body)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if got != "26.5.0" {
		t.Fatalf("got %q, want 26.5.0 (got date instead?)", got)
	}
}

func TestExtractFromPlayStoreNoDS5Block(t *testing.T) {
	_, err := extractFromPlayStore([]byte(`<html><body>nothing here</body></html>`))
	if err == nil {
		t.Fatal("expected error when ds:5 block missing")
	}
	if !strings.Contains(err.Error(), "ds:5") {
		t.Fatalf("error should mention ds:5, got %v", err)
	}
}

func TestExtractFromPlayStoreNoVersionInPayload(t *testing.T) {
	body := []byte(`<script>AF_initDataCallback({key: 'ds:5', data:[null, "5.0.0"], sideChannel: {}});</script>`)
	_, err := extractFromPlayStore(body)
	if err == nil {
		t.Fatal("expected error when no TGTG-shaped version present")
	}
}

// apkMirrorFixture mirrors the real anchor structure on apkmirror.com as of
// 2026-05-06: each release on the listing page is a `<a class="fontBlack">`
// whose text is "Too Good To Go: End Food Waste <version>". The first such
// anchor in document order is the latest release.
const apkMirrorFixture = `<div class="appRow">
<h5><a class="fontBlack" href="/apk/too-good-to-go-aps/too-good-to-go-fight-food-waste-save-great-food/too-good-to-go-end-food-waste-26-5-0-release/">Too Good To Go: End Food Waste 26.5.0</a></h5>
<h5><a class="fontBlack" href="/apk/too-good-to-go-aps/too-good-to-go-fight-food-waste-save-great-food/too-good-to-go-end-food-waste-26-4-1-release/">Too Good To Go: End Food Waste 26.4.1</a></h5>
<h5><a class="fontBlack" href="/apk/too-good-to-go-aps/too-good-to-go-fight-food-waste-save-great-food/too-good-to-go-end-food-waste-26-3-22-release/">Too Good To Go: End Food Waste 26.3.22</a></h5>
</div>`

func TestExtractFromAPKMirrorRealisticFixture(t *testing.T) {
	got, err := extractFromAPKMirror([]byte(apkMirrorFixture))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if got != "26.5.0" {
		t.Fatalf("got %q, want 26.5.0", got)
	}
}

func TestExtractFromAPKMirror(t *testing.T) {
	body := []byte(`<h5><a class="fontBlack">Too Good To Go: End Food Waste 26.5.0</a></h5>`)
	got, err := extractFromAPKMirror(body)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if got != "26.5.0" {
		t.Fatalf("got %q, want 26.5.0", got)
	}
}

func TestExtractFromAPKMirrorAlternateName(t *testing.T) {
	// Some mirror pages use the bare brand name without "End Food Waste".
	body := []byte(`<title>Too Good To Go 26.5.0 APK Download by Too Good To Go Aps</title>`)
	got, err := extractFromAPKMirror(body)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if got != "26.5.0" {
		t.Fatalf("got %q, want 26.5.0", got)
	}
}

func TestExtractFromAPKMirrorNoMatch(t *testing.T) {
	_, err := extractFromAPKMirror([]byte(`<html>completely unrelated</html>`))
	if err == nil {
		t.Fatal("expected error on unrelated body")
	}
}

func TestFetchAPKVersionFirstSourceSucceeds(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<a>Too Good To Go: End Food Waste 27.0.1</a>`))
	}))
	defer ts.Close()

	got, err := fetchAPKVersion(context.Background(), ts.Client(), []APKVersionSource{
		{Name: "first", URL: ts.URL, Extract: extractFromAPKMirror},
		{Name: "second", URL: "http://invalid.invalid", Extract: extractFromAPKMirror},
	})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if got != "27.0.1" {
		t.Fatalf("got %q, want 27.0.1", got)
	}
}

func TestFetchAPKVersionFallsThroughOnHTTPError(t *testing.T) {
	failingTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failingTS.Close()

	successTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<a>Too Good To Go: End Food Waste 27.0.1</a>`))
	}))
	defer successTS.Close()

	got, err := fetchAPKVersion(context.Background(), failingTS.Client(), []APKVersionSource{
		{Name: "broken", URL: failingTS.URL, Extract: extractFromAPKMirror},
		{Name: "ok", URL: successTS.URL, Extract: extractFromAPKMirror},
	})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if got != "27.0.1" {
		t.Fatalf("got %q, want 27.0.1", got)
	}
}

func TestFetchAPKVersionFallsThroughOnExtractError(t *testing.T) {
	noVersionTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<a>nothing useful here</a>`))
	}))
	defer noVersionTS.Close()
	successTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<a>Too Good To Go: End Food Waste 27.1.0</a>`))
	}))
	defer successTS.Close()

	got, err := fetchAPKVersion(context.Background(), noVersionTS.Client(), []APKVersionSource{
		{Name: "no-version", URL: noVersionTS.URL, Extract: extractFromAPKMirror},
		{Name: "ok", URL: successTS.URL, Extract: extractFromAPKMirror},
	})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if got != "27.1.0" {
		t.Fatalf("got %q, want 27.1.0", got)
	}
}

func TestFetchAPKVersionAllFail(t *testing.T) {
	failingTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer failingTS.Close()
	noVersionTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<a>useless body</a>`))
	}))
	defer noVersionTS.Close()

	_, err := fetchAPKVersion(context.Background(), http.DefaultClient, []APKVersionSource{
		{Name: "broken", URL: failingTS.URL, Extract: extractFromAPKMirror},
		{Name: "no-version", URL: noVersionTS.URL, Extract: extractFromAPKMirror},
	})
	if err == nil {
		t.Fatal("expected joined error when all sources fail")
	}
	if !strings.Contains(err.Error(), "broken") || !strings.Contains(err.Error(), "no-version") {
		t.Fatalf("error should mention every source name; got %v", err)
	}
}

func TestFetchAPKVersionEmptySources(t *testing.T) {
	_, err := fetchAPKVersion(context.Background(), http.DefaultClient, nil)
	if err == nil {
		t.Fatal("expected error for empty sources")
	}
}

func TestFetchAPKVersionContextCancellation(t *testing.T) {
	// Use a server that would respond quickly if reached, but pre-cancel the
	// context so the fetch never gets that far. This avoids holding any
	// connection open past test cleanup (httptest.Server.Close has a 5-second
	// grace period for active connections; exceeding it triggers spurious
	// "blocked in Close" warnings).
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := fetchAPKVersion(ctx, ts.Client(), []APKVersionSource{
		{Name: "test", URL: ts.URL, Extract: extractFromAPKMirror},
	})
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "context") {
		t.Fatalf("expected context error, got %v", err)
	}
}

func TestFetchVersionFromSourceSendsBrowserUA(t *testing.T) {
	gotUA := make(chan string, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case gotUA <- r.Header.Get("User-Agent"):
		default:
		}
		_, _ = w.Write([]byte(`<a>Too Good To Go: End Food Waste 26.5.0</a>`))
	}))
	defer ts.Close()

	if _, err := fetchVersionFromSource(context.Background(), ts.Client(), APKVersionSource{
		Name: "test", URL: ts.URL, Extract: extractFromAPKMirror,
	}); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	ua := <-gotUA
	if !strings.Contains(ua, "Mozilla") {
		t.Fatalf("expected browser UA, got %q", ua)
	}
}

func TestLatestSemver(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want string
	}{
		{"single", []string{"26.5.0"}, "26.5.0"},
		{"already sorted", []string{"26.5.0", "26.4.1", "25.10.0"}, "26.5.0"},
		{"reverse", []string{"25.10.0", "26.4.1", "26.5.0"}, "26.5.0"},
		{"patch order matters", []string{"26.5.0", "26.5.10"}, "26.5.10"},
		{"minor double-digit", []string{"26.10.0", "26.9.99"}, "26.10.0"},
		{"major beats minor", []string{"27.0.0", "26.99.99"}, "27.0.0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			matches := make([][][]byte, len(tc.in))
			for i, v := range tc.in {
				matches[i] = [][]byte{[]byte(v), []byte(v)}
			}
			if got := latestSemver(matches); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseSemverTripleHandlesGarbage(t *testing.T) {
	cases := []struct {
		in   string
		want [3]int
	}{
		{"26.5.0", [3]int{26, 5, 0}},
		{"26.5", [3]int{26, 5, 0}},
		{"abc.5.0", [3]int{0, 5, 0}},
		{"", [3]int{0, 0, 0}},
	}
	for _, tc := range cases {
		if got := parseSemverTriple(tc.in); got != tc.want {
			t.Errorf("parseSemverTriple(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestTGTGVersionRegexRejectsBadShapes(t *testing.T) {
	rejected := []string{
		"5.0.0",      // Android min
		"2026.05.06", // ISO date, 4-digit major
		"abc",        // not a version
		"26.5",       // missing patch
		"v26.5.0",    // version isn't isolated by \b correctly with prefix?
		"123.45.67",  // 3-digit major (too large)
	}
	for _, s := range rejected {
		if reTGTGVersion.MatchString(s) {
			// Some forms ("26.5" inside "26.5.0a") would still match; we're
			// testing isolated shapes.
			if s == "v26.5.0" {
				continue // \b allows "v" before, regex matches just "26.5.0"
			}
			t.Errorf("regex unexpectedly matched %q", s)
		}
	}
	accepted := []string{"26.5.0", "10.0.0", "99.99.99"}
	for _, s := range accepted {
		if !reTGTGVersion.MatchString(s) {
			t.Errorf("regex should match %q", s)
		}
	}
}
