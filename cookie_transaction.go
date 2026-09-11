package tgtg

import (
	"net/http"
	"net/url"
	"sync"
)

// cookieTransaction exposes provisional cookies to redirects while keeping
// the established session unchanged until authentication is validated.
type cookieTransaction struct {
	base    *ownedCookieJar
	mu      sync.Mutex
	pending *ownedCookieJar
}

func (j *cookieTransaction) Cookies(u *url.URL) []*http.Cookie {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.snapshot().Cookies(u)
}

func (j *cookieTransaction) SetCookies(u *url.URL, cookies []*http.Cookie) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.snapshot().SetCookies(u, cookies)
}

// The first snapshot is taken after live DataDome acquisition. A 403 discards
// it before refreshing DataDome, so the next attempt sees the refreshed jar.
func (j *cookieTransaction) snapshot() *ownedCookieJar {
	if j.pending == nil {
		j.pending = j.base.clone()
	}
	return j.pending
}

func (j *cookieTransaction) discard() {
	j.mu.Lock()
	j.pending = nil
	j.mu.Unlock()
}

func (j *cookieTransaction) commit() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.pending != nil {
		j.base.replace(j.pending)
		j.pending = nil
	}
}
