package tgtg

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const apkFetchTimeout = 10 * time.Second

// scraperUserAgent imitates a desktop browser. Some sources (notably
// APKMirror) gate non-browser User-Agents.
const scraperUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36"

// APKVersionSource is one fallback step for fetching the latest APK version.
// Exposed so callers can override DefaultAPKVersionSources.
type APKVersionSource struct {
	Name    string
	URL     string
	Extract func(body []byte) (string, error)
}

// rePlayStoreDS5 captures the JSON payload of the "ds:5" data callback in a
// Play Store details page.
var rePlayStoreDS5 = regexp.MustCompile(`AF_initDataCallback\(\{key:\s*'ds:5'.*?data:([\s\S]*?), sideChannel:.+?</script`)

// reTGTGVersion matches a TooGoodToGo-shaped version string. The major must be
// two digits (TGTG has been at major >= 20 since 2020), which excludes Android
// minimum-version strings like "5.0.0" and avoids accidentally picking up
// dates such as "2026.05.06" (whose major has 4 digits).
var reTGTGVersion = regexp.MustCompile(`\b(\d{2}\.\d{1,2}\.\d{1,2})\b`)

// reTGTGVersionQuoted matches the same shape but inside JSON-style quotes. We
// prefer this anchor on the Play Store page because the payload is JSON.
var reTGTGVersionQuoted = regexp.MustCompile(`"(\d{2}\.\d{1,2}\.\d{1,2})"`)

// reTGTGNamedVersion anchors on the app name + a version, used for HTML pages
// where the version sits near the app title. The gap is bounded so we don't
// accidentally pick up an unrelated version far down the page.
var reTGTGNamedVersion = regexp.MustCompile(`(?:Too Good To Go|TooGoodToGo)[\s\S]{0,300}?(\d{2}\.\d{1,2}\.\d{1,2})`)

func extractFromPlayStore(body []byte) (string, error) {
	m := rePlayStoreDS5.FindSubmatch(body)
	if m == nil {
		return "", errors.New("ds:5 block not found in Play Store HTML")
	}
	hits := reTGTGVersionQuoted.FindAllSubmatch(m[1], -1)
	if len(hits) == 0 {
		// Fall back to an unquoted match in case Google reformats the payload.
		hits = reTGTGVersion.FindAllSubmatch(m[1], -1)
	}
	if len(hits) == 0 {
		return "", errors.New("no TGTG-shaped version found in ds:5 payload")
	}
	// Pick the highest semver — defensive against Google ever embedding a
	// version history alongside the current version.
	return latestSemver(hits), nil
}

func extractFromAPKMirror(body []byte) (string, error) {
	hits := reTGTGNamedVersion.FindAllSubmatch(body, -1)
	if len(hits) == 0 {
		return "", errors.New("APKMirror: no TGTG-shaped version found")
	}
	return latestSemver(hits), nil
}

// latestSemver returns the highest version among the regex sub-matches. Each
// match is expected to have the version as its first capture group.
func latestSemver(matches [][][]byte) string {
	var best string
	var bestParts [3]int
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		s := string(m[1])
		parts := parseSemverTriple(s)
		if best == "" || compareSemver(parts, bestParts) > 0 {
			best = s
			bestParts = parts
		}
	}
	return best
}

func parseSemverTriple(s string) [3]int {
	var out [3]int
	for i, p := range strings.SplitN(s, ".", 3) {
		if i >= 3 {
			break
		}
		n, _ := strconv.Atoi(p)
		out[i] = n
	}
	return out
}

func compareSemver(a, b [3]int) int {
	for i := range a {
		if a[i] != b[i] {
			return a[i] - b[i]
		}
	}
	return 0
}

// DefaultAPKVersionSources is the ordered fallback chain used by GetLastAPKVersion.
var DefaultAPKVersionSources = []APKVersionSource{
	{
		Name:    "play.google.com",
		URL:     "https://play.google.com/store/apps/details?id=com.app.tgtg&hl=en&gl=US",
		Extract: extractFromPlayStore,
	},
	{
		Name:    "apkmirror.com",
		URL:     "https://www.apkmirror.com/apk/too-good-to-go-aps/too-good-to-go-fight-food-waste-save-great-food/",
		Extract: extractFromAPKMirror,
	},
}

// GetLastAPKVersion tries DefaultAPKVersionSources in order and returns the
// first version successfully extracted. If every source fails, the joined
// error explains why.
func GetLastAPKVersion(ctx context.Context) (string, error) {
	return fetchAPKVersion(ctx, http.DefaultClient, DefaultAPKVersionSources)
}

// fetchAPKVersion lets tests inject a custom client and source list.
func fetchAPKVersion(ctx context.Context, client *http.Client, sources []APKVersionSource) (string, error) {
	if len(sources) == 0 {
		return "", errors.New("no APK version sources configured")
	}
	var errs []error
	for _, src := range sources {
		v, err := fetchVersionFromSource(ctx, client, src)
		if err == nil {
			return v, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", src.Name, err))
	}
	return "", errors.Join(errs...)
}

func fetchVersionFromSource(ctx context.Context, client *http.Client, src APKVersionSource) (string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, apkFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, src.URL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", scraperUserAgent)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}
	return src.Extract(body)
}
