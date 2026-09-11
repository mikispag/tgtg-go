package tgtg

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"
)

type cookieEntry struct {
	origin url.URL
	cookie http.Cookie
}

// ownedCookieJar retains current cookie metadata in creation order so that an
// authentication transaction can take a private snapshot. Native jars handle
// acceptance, scope identity, matching, and request ordering.
type ownedCookieJar struct {
	mu      sync.Mutex
	now     func() time.Time
	entries []cookieEntry
}

func newOwnedCookieJar(now func() time.Time) *ownedCookieJar {
	return &ownedCookieJar{now: now}
}

func (j *ownedCookieJar) Cookies(u *url.URL) []*http.Cookie {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.prune(j.now())
	native, _ := cookiejar.New(nil)
	for _, entry := range j.entries {
		cookie := withoutCookieExpiry(entry.cookie)
		native.SetCookies(&entry.origin, []*http.Cookie{&cookie})
	}
	return native.Cookies(u)
}

func (j *ownedCookieJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	j.mu.Lock()
	defer j.mu.Unlock()
	now := j.now()
	j.prune(now)
	for _, cookie := range cookies {
		entry := cookieEntry{origin: *u, cookie: *cookie}
		if !strings.HasPrefix(entry.cookie.Path, "/") {
			entry.cookie.Path = "/"
			if i := strings.LastIndex(u.Path, "/"); strings.HasPrefix(u.Path, "/") && i > 0 {
				entry.cookie.Path = u.Path[:i]
			}
		}
		probe, target := cookieProbe(entry, "accepted")
		if len(probe.Cookies(&target)) == 0 {
			continue
		}
		if entry.cookie.MaxAge > 0 {
			entry.cookie.Expires = now.Add(time.Duration(entry.cookie.MaxAge) * time.Second)
			entry.cookie.MaxAge = 0
		}
		remove := entry.cookie.MaxAge < 0 || cookieExpired(entry.cookie, now)
		matched := false
		for i, existing := range j.entries {
			if !sameCookieScope(existing, entry) {
				continue
			}
			if remove {
				copy(j.entries[i:], j.entries[i+1:])
				j.entries[len(j.entries)-1] = cookieEntry{}
				j.entries = j.entries[:len(j.entries)-1]
			} else {
				j.entries[i] = entry
			}
			matched = true
			break
		}
		if !matched && !remove {
			j.entries = append(j.entries, entry)
		}
	}
}

func withoutCookieExpiry(cookie http.Cookie) http.Cookie {
	cookie.MaxAge = 0
	cookie.Expires = time.Time{}
	return cookie
}

func cookieExpired(cookie http.Cookie, now time.Time) bool {
	return !cookie.Expires.IsZero() && !cookie.Expires.After(now)
}

// Probe on the stored path over HTTPS, including cookies that would not be sent
// back to their receipt URL. Expiry is evaluated separately using the owned clock.
func cookieProbe(entry cookieEntry, marker string) (*cookiejar.Jar, url.URL) {
	probe, _ := cookiejar.New(nil)
	cookie := withoutCookieExpiry(entry.cookie)
	cookie.Value = marker
	probe.SetCookies(&entry.origin, []*http.Cookie{&cookie})
	target := entry.origin
	target.Scheme = "https"
	target.Path = cookie.Path
	return probe, target
}

func sameCookieScope(existing, update cookieEntry) bool {
	if existing.cookie.Name != update.cookie.Name || existing.cookie.Path != update.cookie.Path {
		return false
	}
	probe, target := cookieProbe(existing, "original")
	cookie := withoutCookieExpiry(update.cookie)
	cookie.Value = "replacement"
	probe.SetCookies(&update.origin, []*http.Cookie{&cookie})
	for _, cookie := range probe.Cookies(&target) {
		if cookie.Value == "original" {
			return false
		}
	}
	return true
}

func (j *ownedCookieJar) prune(now time.Time) {
	live := j.entries[:0]
	for _, entry := range j.entries {
		if !cookieExpired(entry.cookie, now) {
			live = append(live, entry)
		}
	}
	clear(j.entries[len(live):])
	j.entries = live
}

func (j *ownedCookieJar) clone() *ownedCookieJar {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.prune(j.now())
	return &ownedCookieJar{now: j.now, entries: append([]cookieEntry(nil), j.entries...)}
}

func (j *ownedCookieJar) replace(snapshot *ownedCookieJar) {
	copy := snapshot.clone()
	j.mu.Lock()
	j.entries = copy.entries
	j.mu.Unlock()
}

func seedCookieSnapshot(jar *ownedCookieJar, u *url.URL, cookies []*http.Cookie) {
	seen := make(map[string]bool)
	for _, cookie := range cookies {
		if seen[cookie.Name] {
			continue
		}
		seen[cookie.Name] = true
		copy := *cookie
		copy.Path = "/"
		copy.Secure = u.Scheme == "https"
		jar.SetCookies(u, []*http.Cookie{&copy})
	}
}
