package tgtg

import (
	"net/http"
	"net/url"
	"sync"
)

type cookieUpdate struct {
	url     url.URL
	cookies []*http.Cookie
}

// cookieTransaction sends only established cookies and buffers response
// updates, including redirects. Failed authentication discards the updates.
type cookieTransaction struct {
	base    http.CookieJar
	mu      sync.Mutex
	updates []cookieUpdate
}

func (j *cookieTransaction) Cookies(u *url.URL) []*http.Cookie {
	return j.base.Cookies(u)
}

func (j *cookieTransaction) SetCookies(u *url.URL, cookies []*http.Cookie) {
	update := cookieUpdate{url: *u, cookies: make([]*http.Cookie, len(cookies))}
	for i, cookie := range cookies {
		copy := *cookie
		update.cookies[i] = &copy
	}
	j.mu.Lock()
	j.updates = append(j.updates, update)
	j.mu.Unlock()
}

func (j *cookieTransaction) discard() {
	j.mu.Lock()
	j.updates = nil
	j.mu.Unlock()
}

func (j *cookieTransaction) commit() {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, update := range j.updates {
		j.base.SetCookies(&update.url, update.cookies)
	}
	j.updates = nil
}
