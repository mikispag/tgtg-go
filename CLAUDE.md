# CLAUDE.md

This file provides guidance to agents when working with code in this repository.

## Project Overview

Unofficial Go client for the [TooGoodToGo](https://toogoodtogo.com) API. Module
path `github.com/mikispag/tgtg-go`. Go 1.23+. Licensed under GPL-3.0. Originally
ported from [`tgtg-python`](https://github.com/ahivert/tgtg-python), but the Go
client is its own project — Python is a useful reference for *what* the API
expects on the wire, not a constraint on *how* the Go code is written.

## Guiding Principles

- **Follow Go best practices.** Effective Go, the Go Code Review Comments, and
  the standard library's own style are the bar. Idiomatic Go beats faithful
  Python.
- **Improve on the upstream.** When Python does something awkward (silent
  exception swallowing, mutable defaults, time-arithmetic bugs, magic strings),
  fix it in Go and note the deviation in the commit message or this file.
- **Type safety where it pays.** Prefer typed structs for fields the client
  actually reads or sets. Keep `map[string]any` for genuinely opaque
  pass-through payloads where typing buys nothing — but don't reach for it
  reflexively.
- **Robust error handling.** Wrap with `%w`, return typed errors that callers
  can `errors.As`, never panic on remote input, always close response bodies.
- **Concurrency-safe by construction.** No global mutable state; protect any
  mutable `Client` field touched from multiple goroutines, document goroutine
  safety expectations on public types.
- **Testability is a feature.** Inject clocks, sleeps, I/O, randomness, and
  network endpoints rather than reaching for the real ones. Tests must run
  with `-race` and never touch the public internet.
- **Stdlib first.** Add a third-party dependency only when the standard library
  truly can't do the job, and call out the reason in the PR.

## Commands

```bash
# Build
make build           # or: go build ./...

# Run all tests (race detector enabled, no caching)
make test            # or: go test -race -count=1 ./...

# Run a single test
go test -run TestLoginWithTokens ./...

# Run tests with coverage
go test -cover -count=1 ./...

# Lint (vet + gofmt check)
make lint
```

## Code Style

- Format with `gofmt -s` (enforced by `make lint`).
- `go vet` must be clean.
- Doc-comment every exported identifier. Keep comments accurate to behavior;
  stale comments are worse than missing ones.
- Errors flow up; don't log-and-continue on the request path. The fetch of the
  DataDome cookie is one deliberate exception (best-effort, see below).

## Architecture

Single package. Files:

- **`tgtg.go`** — `Client` struct, `Config`, all endpoint methods, the
  `post()` helper that handles DataDome cookie management + 403 retry, header
  building, UUID/CID generation, default stdin PIN reader.
- **`errors.go`** — `LoginError`, `APIError`, `PollingError` typed errors.
  `APIError.State` is set when an order endpoint returns HTTP 200 but the
  body's `state` field is not `"SUCCESS"`.
- **`apk.go`** — Scrapes the Google Play Store page for the current TooGoodToGo
  APK version. Falls back to `DefaultAPKVersion` on any error. Called lazily
  from `New()` only when no `UserAgent` is provided.

### Conventions

- All public methods take `context.Context` as their first argument and respect
  cancellation.
- Endpoint constants (`AuthByEmailEndpoint`, etc.) are exported and use Go
  format-string syntax (`%s`) for path parameters.
- Constructor `New(Config)` applies defaults for unset fields. Extend `Config`
  rather than adding parallel constructors.
- Methods with many optional parameters take an options struct
  (`GetItemsOptions`, `GetFavoritesOptions`, `SignupOptions`) with a
  `Default*Options()` helper for sensible defaults. Methods with one or two
  required args take them positionally.
- Inject `Now func() time.Time`, `Sleep func(time.Duration)`,
  `PinReader func() (string, error)`, and `Output io.Writer` via `Config` for
  test seams. The client uses these instead of `time.Now`, `time.Sleep`,
  stdin, and stdout directly.
- Goroutine safety: a `*Client` is intended for serial use within a single
  caller (matches typical HTTP-client usage). If a method ever needs to be
  goroutine-safe, document and protect the relevant fields explicitly.

### DataDome bot protection

1. The client fetches a `datadome` cookie from
   `https://api-sdk.datadome.co/sdk/` before API requests, mimicking the
   Android app's DataDome SDK (device fingerprint params: model, OS version,
   screen size, etc.). The fetch is best-effort: failures are logged to
   `Config.Output` and never surfaced to the caller.
2. `post()` calls `ensureDataDomeCookie()` first; on HTTP 403 it clears the
   jar, refetches, and retries once.
3. `generateDataDomeCID()` builds random 120-char client IDs in DataDome's
   expected charset.
4. The DataDome SDK URL is configurable via `Config.DataDomeSDKURL`; tests
   redirect it to the mock server so it 404s instead of touching the real
   internet.
5. VPN / datacenter IPs are frequently blocked regardless of cookie validity —
   residential IPs work best.

### Authentication flow

1. Email-based login → `auth/v5/authByEmail`.
2. User receives a PIN via email.
3. `Config.PinReader` returns the PIN; client submits it via
   `auth/v5/authByRequestPin`.
4. If `PinReader` returns an empty string, the client falls back to polling
   `auth/v5/authByRequestPollingId` (legacy link-click flow), up to
   `MaxPollingTries` × `PollingWaitTime`.
5. On success, `AccessToken`, `RefreshToken`, and `Cookie` are stored on the
   client.
6. `LastTimeTokenRefreshed` controls auto-refresh: `Login()` re-uses tokens
   while `Now().Sub(LastTimeTokenRefreshed) <= AccessTokenLifetime`, otherwise
   posts to `token/v1/refresh`. Note: this uses the full duration, not just the
   seconds component — a deliberate fix relative to the upstream Python.

### API versions in use

Auth `v5`, items `v8`, orders `v8`, token refresh `v1`, favorites `v1`,
discover `v1`. Bump in lockstep when TooGoodToGo rolls a new version.

## Testing

- Standard library only: `net/http/httptest` plus a small `mockServer` helper
  in `tgtg_test.go`.
- Each test spins up its own mock server via `newMockServer(t)`; routes are
  registered with `addJSON(method, path, status, body, headers)` and replaced
  with `replaceJSON(...)`.
- Use `newClient(t, m, cfg)` to build a `Client` wired to the mock server with
  a deterministic `UserAgent`, no-op `Sleep`, and discarded `Output`. The
  helper also redirects `DataDomeSDKURL` to the mock server so it doesn't
  escape to the real internet.
- Tests that depend on time use `Config.Now = func() time.Time { return cur }`
  and mutate `cur` between calls.
- Always run `go test -race -count=1 ./...` before claiming a fix; the mock
  server is hit concurrently in some tests.
- Add tests for new branches and for context-cancellation paths when adding
  long-running flows (polling, retries).

## Adding or modifying endpoints

1. Add or update the endpoint constant in `tgtg.go` (use `%s` placeholders for
   path params; exported names).
2. Add the method on `*Client`:
   - `context.Context` first.
   - Call `c.Login(ctx)` first if authentication is required.
   - Use `c.post(ctx, c.urlFor(endpoint), body)` for the request.
   - On a non-2xx response, return `*APIError`. On a 200 with a non-`SUCCESS`
     state where applicable, return `*APIError` with `State` set.
   - Use a typed struct for the response if the caller will read named fields;
     fall back to `map[string]any` only when the body is genuinely opaque.
3. Add tests covering the success path, every failure branch, and any
   request-body invariant the endpoint depends on.
4. Update `README.md` if the public API changed.
5. Run `make lint && make test`.
