// Package tgtg is an unofficial Go client for the TooGoodToGo API.
package tgtg

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	mathrand "math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	// BaseURL is the default TooGoodToGo API URL.
	BaseURL = "https://apptoogoodtogo.com/api/"
	// APIItemEndpoint lists items or retrieves an item when its ID is appended.
	APIItemEndpoint = "item/v9/"
	// FavoriteItemEndpoint updates a favorite; %s is the item ID.
	FavoriteItemEndpoint = "user/favorite/v1/%s/update"
	// AuthByEmailEndpoint starts email authentication.
	AuthByEmailEndpoint = "auth/v5/authByEmail"
	// AuthPollingEndpoint polls for completed email authentication.
	AuthPollingEndpoint = "auth/v5/authByRequestPollingId"
	// AuthByRequestPinEndpoint exchanges an email PIN for credentials.
	AuthByRequestPinEndpoint = "auth/v5/authByRequestPin"
	// SignupByEmailEndpoint registers an account by email.
	SignupByEmailEndpoint = "auth/v5/signUpByEmail"
	// RefreshEndpoint exchanges a refresh token for new credentials.
	RefreshEndpoint = "token/v1/refresh"
	// ActiveOrderEndpoint lists active orders.
	ActiveOrderEndpoint = "order/v8/active"
	// InactiveOrderEndpoint lists inactive orders.
	InactiveOrderEndpoint = "order/v8/inactive"
	// CreateOrderEndpoint reserves an item when its ID is appended.
	CreateOrderEndpoint = "order/v8/create/"
	// AbortOrderEndpoint cancels an order; %s is the order ID.
	AbortOrderEndpoint = "order/v8/%s/abort"
	// OrderStatusEndpoint retrieves order status; %s is the order ID.
	OrderStatusEndpoint = "order/v8/%s/status"
	// APIBucketEndpoint retrieves a discovery bucket, including favorites.
	APIBucketEndpoint = "discover/v1/bucket"
	// ManufacturerItemEndpoint retrieves manufacturer items.
	ManufacturerItemEndpoint = "manufactureritem/v2"
	// DataDomeSDKURL is the default endpoint for acquiring DataDome cookies.
	DataDomeSDKURL = "https://api-sdk.datadome.co/sdk/"

	// DefaultAPKVersion is used when APK version discovery fails.
	DefaultAPKVersion = "26.8.0"
	// DefaultAccessTokenLifetime is the default interval between token refreshes.
	DefaultAccessTokenLifetime = 4 * time.Hour
	// MaxPollingTries limits attempts to complete email authentication by polling.
	MaxPollingTries = 24
	// PollingWaitTime is the delay between email authentication polling attempts.
	PollingWaitTime = 5 * time.Second
)

var userAgentTemplates = []string{
	"TGTG/%s Dalvik/2.1.0 (Linux; U; Android 9; Nexus 5 Build/M4B30Z)",
	"TGTG/%s Dalvik/2.1.0 (Linux; U; Android 10; SM-G935F Build/NRD90M)",
	"TGTG/%s Dalvik/2.1.0 (Linux; Android 12; SM-G920V Build/MMB29K)",
}

// httpResponse is the value-type result of a successful POST. Returning it by
// value (rather than *http.Response) makes the contract that "no error => valid
// fields" enforced by the type system.
type httpResponse struct {
	StatusCode int
	Header     http.Header
	Body       []byte
	cookies    *cookieTransaction
}

const dataDomeCIDChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789~_"

// Credentials holds the tokens and cookies returned by GetCredentials.
type Credentials struct {
	// AccessToken authenticates API requests.
	AccessToken string `json:"access_token"`
	// RefreshToken renews the access token.
	RefreshToken string `json:"refresh_token"`
	// Cookie contains request cookie pairs for the API origin, without attributes.
	Cookie string `json:"cookie"`
}

// Config configures a Client. All fields are optional.
type Config struct {
	// URL is the API base URL, with an optional trailing slash. It defaults to BaseURL.
	URL string
	// Email is the account address used for email authentication.
	Email string
	// AccessToken is a previously issued API access token.
	AccessToken string
	// RefreshToken renews AccessToken; supply both tokens to restore a session.
	RefreshToken string
	// Cookie contains request cookie pairs to restore into the private cookie jar.
	Cookie string
	// UserAgent overrides the generated User-Agent and skips APK version discovery.
	UserAgent string
	// Language is the API language preference. It defaults to en-GB.
	Language string
	// DeviceType identifies the client platform. It defaults to ANDROID.
	DeviceType string
	// APKVersion sets the version used to generate UserAgent, skipping discovery.
	APKVersion string
	// AccessTokenLifetime controls when tokens are refreshed. Zero selects the default.
	AccessTokenLifetime time.Duration
	// LastTimeTokenRefreshed is the last token refresh time. Zero triggers a refresh.
	LastTimeTokenRefreshed time.Time
	// Timeout sets the HTTP request timeout; zero preserves HTTPClient's timeout.
	Timeout time.Duration
	// HTTPClient supplies transport, redirect policy, and timeout settings.
	// New copies it and seeds a private jar with its cookies for URL.
	// A nonzero Timeout overrides its timeout. The supplied client is not mutated.
	HTTPClient *http.Client
	// DataDomeSDKURL overrides the DataDome cookie endpoint.
	DataDomeSDKURL string
	// PinReader returns the PIN entered by the user during email login. If it
	// returns an empty string, the client falls back to the legacy polling flow.
	// A callback can finish after cancellation; only one remains in flight.
	PinReader func() (string, error)
	// PinReaderContext takes precedence over PinReader and must honor ctx.
	// An empty PIN selects polling. Prefer it for cancellable input sources.
	PinReaderContext func(ctx context.Context) (string, error)
	// Now supplies the time for token and cookie expiry. It defaults to time.Now.
	Now func() time.Time
	// Sleep overrides polling delays. Prefer SleepContext for cancellable waits.
	// Legacy callbacks can finish after cancellation; only one remains in flight.
	Sleep func(time.Duration)
	// SleepContext takes precedence over Sleep and must honor ctx.
	// With neither set, polling uses a cancellable timer.
	SleepContext func(ctx context.Context, delay time.Duration) error
	// Output controls where login progress messages are written. Defaults to os.Stdout.
	Output io.Writer
	// APKVersionFetcher fetches the latest TooGoodToGo APK version. It is
	// invoked lazily on the first request when neither UserAgent nor APKVersion
	// is supplied, and receives the request's context. Defaults to version
	// discovery through the configured HTTP client.
	APKVersionFetcher func(ctx context.Context) (string, error)
}

// Client talks to the TooGoodToGo API. It is intended for serial use; callers
// must synchronize access to its methods and fields. Separate clients may
// share an HTTP transport while maintaining independent cookie jars.
type Client struct {
	// BaseURL is the API base URL.
	BaseURL string
	// Email is the account address used for email authentication.
	Email string
	// AccessToken is the current API access token.
	AccessToken string
	// RefreshToken renews AccessToken.
	RefreshToken string
	// Cookie is a snapshot of request cookie pairs from the private cookie jar.
	// Use Config.Cookie to restore cookies when creating a client.
	Cookie string
	// UserAgent is the User-Agent header sent with API requests.
	UserAgent string
	// Language is the API language preference.
	Language string
	// DeviceType identifies the client platform.
	DeviceType string
	// APKVersion is the configured or discovered app version.
	APKVersion string
	// AccessTokenLifetime controls when tokens are refreshed.
	AccessTokenLifetime time.Duration
	// LastTimeTokenRefreshed records the last successful token update.
	LastTimeTokenRefreshed time.Time
	// Timeout is the configured HTTP request timeout.
	Timeout time.Duration

	correlationID       string
	httpClient          *http.Client
	dataDomeSDKURL      string
	pinReader           func() (string, error)
	pinReaderContext    func(context.Context) (string, error)
	promptPIN           bool
	pendingPIN          <-chan pinResult
	pendingPINEmail     string
	pendingPINPollingID string
	now                 func() time.Time
	sleep               func(time.Duration)
	sleepContext        func(context.Context, time.Duration) error
	pendingSleep        <-chan struct{}
	out                 io.Writer
	apkVersionFetcher   func(ctx context.Context) (string, error)
}

// New creates a Client using cfg, applying defaults for unset fields.
func New(cfg Config) *Client {
	if cfg.URL == "" {
		cfg.URL = BaseURL
	}
	cfg.URL = strings.TrimRight(cfg.URL, "/") + "/"
	if cfg.Language == "" {
		cfg.Language = "en-GB"
	}
	if cfg.DeviceType == "" {
		cfg.DeviceType = "ANDROID"
	}
	if cfg.AccessTokenLifetime == 0 {
		cfg.AccessTokenLifetime = DefaultAccessTokenLifetime
	}
	if cfg.DataDomeSDKURL == "" {
		cfg.DataDomeSDKURL = DataDomeSDKURL
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Sleep == nil && cfg.SleepContext == nil {
		cfg.SleepContext = sleepWithContext
	}
	if cfg.Output == nil {
		cfg.Output = os.Stdout
	}
	promptPIN := cfg.PinReader == nil && cfg.PinReaderContext == nil
	if promptPIN {
		in := bufio.NewReader(os.Stdin)
		cfg.PinReader = func() (string, error) {
			return stdinPinReader(io.Discard, in)
		}
	}

	httpClient := &http.Client{}
	if cfg.HTTPClient != nil {
		*httpClient = *cfg.HTTPClient
	}
	if cfg.Timeout != 0 {
		httpClient.Timeout = cfg.Timeout
	}
	cfg.Timeout = httpClient.Timeout
	jar := newOwnedCookieJar(cfg.Now)
	if apiURL, err := url.Parse(cfg.URL); err == nil {
		if httpClient.Jar != nil {
			seedCookieSnapshot(jar, apiURL, httpClient.Jar.Cookies(apiURL))
		}
		req := &http.Request{Header: http.Header{"Cookie": {cfg.Cookie}}}
		seedCookieSnapshot(jar, apiURL, req.Cookies())
	}
	httpClient.Jar = jar
	if cfg.APKVersionFetcher == nil {
		cfg.APKVersionFetcher = func(ctx context.Context) (string, error) {
			return fetchAPKVersion(ctx, httpClient, DefaultAPKVersionSources)
		}
	}

	c := &Client{
		BaseURL:                cfg.URL,
		Email:                  cfg.Email,
		AccessToken:            cfg.AccessToken,
		RefreshToken:           cfg.RefreshToken,
		Cookie:                 cfg.Cookie,
		UserAgent:              cfg.UserAgent,
		Language:               cfg.Language,
		DeviceType:             cfg.DeviceType,
		APKVersion:             cfg.APKVersion,
		AccessTokenLifetime:    cfg.AccessTokenLifetime,
		LastTimeTokenRefreshed: cfg.LastTimeTokenRefreshed,
		Timeout:                cfg.Timeout,

		correlationID:     newUUID(),
		httpClient:        httpClient,
		dataDomeSDKURL:    cfg.DataDomeSDKURL,
		pinReader:         cfg.PinReader,
		pinReaderContext:  cfg.PinReaderContext,
		promptPIN:         promptPIN,
		now:               cfg.Now,
		sleep:             cfg.Sleep,
		sleepContext:      cfg.SleepContext,
		out:               cfg.Output,
		apkVersionFetcher: cfg.APKVersionFetcher,
	}
	c.syncCookies()

	// When the APK version is already known, build the User-Agent eagerly —
	// no I/O is required. Otherwise defer resolution to the first request,
	// where the caller's context governs the APK version fetch.
	if c.UserAgent == "" && c.APKVersion != "" {
		c.resolveUserAgent(context.Background())
	}
	return c
}

// resolveUserAgent populates c.UserAgent if not already set. When APKVersion
// is empty, it fetches the latest version using ctx, falling back to
// DefaultAPKVersion on failure.
func (c *Client) resolveUserAgent(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.UserAgent != "" {
		return nil
	}
	if c.APKVersion == "" {
		v, err := c.apkVersionFetcher(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil || v == "" {
			c.APKVersion = DefaultAPKVersion
			fmt.Fprintln(c.out, "Failed to get last version")
		} else {
			c.APKVersion = v
		}
	}
	fmt.Fprintf(c.out, "Using version %s\n", c.APKVersion)
	tmpl := userAgentTemplates[mathrand.IntN(len(userAgentTemplates))]
	c.UserAgent = fmt.Sprintf(tmpl, c.APKVersion)
	return nil
}

func (c *Client) buildHeaders() http.Header {
	h := http.Header{}
	h.Set("Accept", "application/json")
	h.Set("Accept-Encoding", "gzip")
	h.Set("Accept-Language", c.Language)
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("User-Agent", c.UserAgent)
	h.Set("X-Correlation-ID", c.correlationID)
	if c.AccessToken != "" {
		h.Set("Authorization", "Bearer "+c.AccessToken)
	}
	return h
}

func (c *Client) alreadyLogged() bool {
	return c.AccessToken != "" && c.RefreshToken != ""
}

func (c *Client) urlFor(path string) string {
	return strings.TrimRight(c.BaseURL, "/") + "/" + path
}

// escapeID keeps an identifier in one path segment, including dot segments.
func escapeID(id string) string {
	if id == "." || id == ".." {
		return strings.ReplaceAll(id, ".", "%2E")
	}
	return url.PathEscape(id)
}

// post sends a POST with JSON body, ensuring a DataDome cookie is attempted
// and retrying once with a fresh cookie on a 403.
func (c *Client) post(ctx context.Context, requestURL string, body any) (httpResponse, error) {
	return c.postWithClient(ctx, requestURL, body, c.httpClient)
}

func (c *Client) postWithClient(ctx context.Context, requestURL string, body any, client *http.Client) (httpResponse, error) {
	if err := c.resolveUserAgent(ctx); err != nil {
		return httpResponse{}, err
	}
	c.ensureDataDomeCookie(ctx, requestURL)
	res, err := c.doPostWithClient(ctx, requestURL, body, client)
	if err != nil {
		return httpResponse{}, err
	}
	if res.StatusCode == http.StatusForbidden {
		if cookies, ok := client.Jar.(*cookieTransaction); ok {
			cookies.discard()
		}
		c.clearDataDomeCookie(requestURL)
		c.fetchDataDomeCookie(ctx, requestURL)
		res, err = c.doPostWithClient(ctx, requestURL, body, client)
		if err != nil {
			return httpResponse{}, err
		}
	}
	return res, nil
}

func (c *Client) doPost(ctx context.Context, requestURL string, body any) (httpResponse, error) {
	return c.doPostWithClient(ctx, requestURL, body, c.httpClient)
}

func (c *Client) doPostWithClient(ctx context.Context, requestURL string, body any, client *http.Client) (httpResponse, error) {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return httpResponse{}, fmt.Errorf("marshal body: %w", err)
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, reader)
	if err != nil {
		return httpResponse{}, err
	}
	req.Header = c.buildHeaders()
	resp, err := client.Do(req)
	if err != nil {
		return httpResponse{}, err
	}
	defer resp.Body.Close()
	c.syncCookies()
	payload, err := readResponseBody(resp, maxAPIResponseBytes)
	if err != nil {
		return httpResponse{}, fmt.Errorf("read response body: %w", err)
	}
	return httpResponse{
		StatusCode: resp.StatusCode,
		Header:     resp.Header,
		Body:       payload,
	}, nil
}

func (c *Client) ensureDataDomeCookie(ctx context.Context, requestURL string) {
	u, err := url.Parse(requestURL)
	if err != nil {
		return
	}
	for _, ck := range c.httpClient.Jar.Cookies(u) {
		if ck.Name == "datadome" {
			return
		}
	}
	c.fetchDataDomeCookie(ctx, requestURL)
}

func (c *Client) syncCookies() {
	u, err := url.Parse(c.urlFor(""))
	if err != nil {
		return
	}
	req := &http.Request{Header: make(http.Header)}
	for _, ck := range c.httpClient.Jar.Cookies(u) {
		req.AddCookie(ck)
	}
	c.Cookie = req.Header.Get("Cookie")
}

func (c *Client) clearDataDomeCookie(requestURL string) {
	u, err := url.Parse(requestURL)
	if err != nil {
		return
	}
	// CookieJar exposes request cookies without their original domain or path.
	// Expire each scope that could match this request, preserving other cookies.
	for domain := strings.TrimSuffix(u.Hostname(), "."); domain != ""; {
		cookieDomain := domain
		if net.ParseIP(domain) != nil {
			cookieDomain = ""
		}
		path := u.Path
		if path == "" {
			path = "/"
		}
		for {
			c.httpClient.Jar.SetCookies(u, []*http.Cookie{{
				Name: "datadome", Domain: cookieDomain, Path: path, MaxAge: -1,
			}})
			if path == "/" {
				break
			}
			if strings.HasSuffix(path, "/") {
				path = strings.TrimSuffix(path, "/")
			} else {
				path = path[:strings.LastIndex(path, "/")+1]
			}
		}
		if net.ParseIP(domain) != nil {
			break
		}
		_, domain, _ = strings.Cut(domain, ".")
	}
	c.syncCookies()
}

func (c *Client) fetchDataDomeCookie(ctx context.Context, requestURL string) {
	apkVersion := c.APKVersion
	if apkVersion == "" {
		apkVersion = DefaultAPKVersion
	}
	form := url.Values{}
	form.Set("camera", `{"auth":"true", "info":"{\"front\":\"2000x1500\",\"back\":\"5472x3648\"}"}`)
	form.Set("cid", generateDataDomeCID())
	form.Set("ddk", "1D42C2CA6131C526E09F294FE96F94")
	form.Set("ddv", "3.0.4")
	form.Set("ddvc", apkVersion)
	form.Set("events", fmt.Sprintf(`[{"id":1,"message":"response validation","source":"sdk","date":%d}]`, c.now().UnixMilli()))
	form.Set("inte", "android-java-okhttp")
	form.Set("mdl", "Pixel 7 Pro")
	form.Set("os", "Android")
	form.Set("osn", "UPSIDE_DOWN_CAKE")
	form.Set("osr", "14")
	form.Set("osv", "34")
	form.Set("request", requestURL)
	form.Set("screen_d", "3.5")
	form.Set("screen_x", "1440")
	form.Set("screen_y", "3120")
	form.Set("ua", c.UserAgent)

	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.dataDomeSDKURL, strings.NewReader(form.Encode()))
	if err != nil {
		fmt.Fprintln(c.out, "Failed to fetch DataDome cookie")
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", c.UserAgent)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		fmt.Fprintln(c.out, "Failed to fetch DataDome cookie")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	body, err := readResponseBody(resp, maxDataDomeResponseBytes)
	if err != nil {
		return
	}
	var data struct {
		Status int    `json:"status"`
		Cookie string `json:"cookie"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return
	}
	if data.Status != 200 || data.Cookie == "" {
		return
	}
	ck, err := http.ParseSetCookie(data.Cookie)
	if err != nil || ck.Name != "datadome" || ck.Value == "" {
		return
	}
	apiURL, err := url.Parse(c.BaseURL)
	if err != nil {
		return
	}
	ck.Domain = ""
	ck.Path = "/"
	ck.Secure = apiURL.Scheme == "https"
	c.httpClient.Jar.SetCookies(apiURL, []*http.Cookie{ck})
	c.syncCookies()
}

// Login authenticates the client using an email or both access and refresh
// tokens. Saved cookies are optional. When both tokens are present, Login
// refreshes them only when expired. Without both tokens, it starts email login.
func (c *Client) Login(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.Email == "" && !c.alreadyLogged() {
		return &LoginError{Err: errors.New("provide an email or both access_token and refresh_token")}
	}
	if c.alreadyLogged() {
		return c.refreshToken(ctx)
	}
	if c.pendingPIN != nil && c.pendingPINPollingID != "" {
		if c.pendingPINEmail == c.Email {
			return c.startPolling(ctx, c.pendingPINPollingID)
		}
		if err := c.discardPendingPIN(ctx); err != nil {
			return err
		}
	}
	resp, err := c.postAuth(ctx, c.urlFor(AuthByEmailEndpoint), map[string]any{
		"device_type": c.DeviceType,
		"email":       c.Email,
	})
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusOK {
		var first struct {
			State     string `json:"state"`
			PollingID string `json:"polling_id"`
		}
		if err := json.Unmarshal(resp.Body, &first); err != nil {
			return &LoginError{StatusCode: resp.StatusCode, Body: string(resp.Body), Err: fmt.Errorf("decode email login response: %w", err)}
		}
		switch first.State {
		case "TERMS":
			return &PollingError{Message: fmt.Sprintf(
				"This email %s is not linked to a tgtg account. Please signup with this email first.", c.Email)}
		case "WAIT":
			if first.PollingID == "" {
				return &LoginError{StatusCode: resp.StatusCode, Body: string(resp.Body), Err: errors.New("missing polling_id")}
			}
			c.commitAuthCookies(resp)
			return c.startPolling(ctx, first.PollingID)
		default:
			return &LoginError{StatusCode: resp.StatusCode, Body: string(resp.Body)}
		}
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return &APIError{StatusCode: resp.StatusCode, Body: "Too many requests. Try again later."}
	}
	return &LoginError{StatusCode: resp.StatusCode, Body: string(resp.Body)}
}

func (c *Client) refreshToken(ctx context.Context) error {
	if !c.LastTimeTokenRefreshed.IsZero() && c.now().Sub(c.LastTimeTokenRefreshed) <= c.AccessTokenLifetime {
		return nil
	}
	resp, err := c.postAuth(ctx, c.urlFor(RefreshEndpoint), map[string]any{
		"refresh_token": c.RefreshToken,
	})
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return &APIError{StatusCode: resp.StatusCode, Body: string(resp.Body)}
	}
	var tok authTokens
	if err := json.Unmarshal(resp.Body, &tok); err != nil {
		return &APIError{StatusCode: resp.StatusCode, Body: string(resp.Body), Err: fmt.Errorf("decode token refresh response: %w", err)}
	}
	if err := tok.validate(); err != nil {
		return &APIError{StatusCode: resp.StatusCode, Body: string(resp.Body), Err: err}
	}
	c.storeTokens(tok, resp)
	return nil
}

func (c *Client) startPolling(ctx context.Context, pollingID string) error {
	fmt.Fprintln(c.out, "Check your email for a login PIN code.")
	c.pendingPINEmail = c.Email
	c.pendingPINPollingID = pollingID
	pin, err := c.readPIN(ctx)
	if c.pendingPIN == nil {
		c.pendingPINEmail = ""
		c.pendingPINPollingID = ""
	}
	if err != nil {
		return err
	}
	pin = strings.TrimSpace(pin)
	if pin != "" {
		return c.authByPIN(ctx, pollingID, pin)
	}
	for i := 0; i < MaxPollingTries; i++ {
		resp, err := c.postAuth(ctx, c.urlFor(AuthPollingEndpoint), map[string]any{
			"device_type":        c.DeviceType,
			"email":              c.Email,
			"request_polling_id": pollingID,
		})
		if err != nil {
			return err
		}
		switch resp.StatusCode {
		case http.StatusAccepted:
			c.commitAuthCookies(resp)
			fmt.Fprintln(c.out, "Check your mailbox on PC to continue... "+
				"(Opening email on mobile won't work, if you have installed tgtg app.)")
			if i+1 < MaxPollingTries {
				if err := c.wait(ctx, PollingWaitTime); err != nil {
					return err
				}
			}
			continue
		case http.StatusOK:
			var tok authTokens
			if err := json.Unmarshal(resp.Body, &tok); err != nil {
				return &LoginError{StatusCode: resp.StatusCode, Body: string(resp.Body), Err: fmt.Errorf("decode polling response: %w", err)}
			}
			if err := tok.validate(); err != nil {
				return &LoginError{StatusCode: resp.StatusCode, Body: string(resp.Body), Err: err}
			}
			c.storeTokens(tok, resp)
			fmt.Fprintln(c.out, "Logged in!")
			return nil
		case http.StatusTooManyRequests:
			return &APIError{StatusCode: resp.StatusCode, Body: "Too many requests. Try again later."}
		default:
			return &LoginError{StatusCode: resp.StatusCode, Body: string(resp.Body)}
		}
	}
	return &PollingError{Message: fmt.Sprintf(
		"Max retries (%d attempts) reached. Try again.", MaxPollingTries)}
}

func (c *Client) authByPIN(ctx context.Context, pollingID, pin string) error {
	resp, err := c.postAuth(ctx, c.urlFor(AuthByRequestPinEndpoint), map[string]any{
		"device_type":        c.DeviceType,
		"email":              c.Email,
		"request_pin":        pin,
		"request_polling_id": pollingID,
	})
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return &LoginError{StatusCode: resp.StatusCode, Body: string(resp.Body)}
	}
	var tok authTokens
	if err := json.Unmarshal(resp.Body, &tok); err != nil {
		return &LoginError{StatusCode: resp.StatusCode, Body: string(resp.Body), Err: fmt.Errorf("decode PIN response: %w", err)}
	}
	if err := tok.validate(); err != nil {
		return &LoginError{StatusCode: resp.StatusCode, Body: string(resp.Body), Err: err}
	}
	c.storeTokens(tok, resp)
	fmt.Fprintln(c.out, "Logged in!")
	return nil
}

// GetCredentials logs in (if needed) and returns the current tokens.
func (c *Client) GetCredentials(ctx context.Context) (Credentials, error) {
	if err := c.Login(ctx); err != nil {
		return Credentials{}, err
	}
	c.syncCookies()
	return Credentials{
		AccessToken:  c.AccessToken,
		RefreshToken: c.RefreshToken,
		Cookie:       c.Cookie,
	}, nil
}

// GetItemsOptions configures the location, paging, and filters for GetItems.
type GetItemsOptions struct {
	// Latitude is the search origin's latitude in degrees.
	Latitude float64
	// Longitude is the search origin's longitude in degrees.
	Longitude float64
	// Radius is the search radius sent to the API.
	Radius int
	// PageSize is the requested number of items per page.
	PageSize int
	// Page is the requested page number.
	Page int
	// Discover enables the API's discover filter.
	Discover bool
	// FavoritesOnly restricts results to favorites.
	FavoritesOnly bool
	// ItemCategories restricts results to the supplied item category identifiers.
	ItemCategories []string
	// DietCategories restricts results to the supplied dietary category identifiers.
	DietCategories []string
	// PickupEarliest sets the earliest pickup time; nil leaves it unrestricted.
	PickupEarliest *time.Time
	// PickupLatest sets the latest pickup time; nil leaves it unrestricted.
	PickupLatest *time.Time
	// SearchPhrase filters results by text; an empty string leaves it unrestricted.
	SearchPhrase string
	// WithStockOnly restricts results to items with available stock.
	WithStockOnly bool
	// HiddenOnly enables the API's hidden_only filter.
	HiddenOnly bool
	// WeCareOnly enables the API's we_care_only filter.
	WeCareOnly bool
}

// DefaultGetItemsOptions returns options matching the Python defaults
// (favorites_only=true, page_size=20, page=1, radius=21).
func DefaultGetItemsOptions() GetItemsOptions {
	return GetItemsOptions{
		Radius:        21,
		PageSize:      20,
		Page:          1,
		FavoritesOnly: true,
	}
}

// GetItems lists items using opts. Use DefaultGetItemsOptions for defaults,
// including restricting results to favorites; zero options do not apply defaults.
func (c *Client) GetItems(ctx context.Context, opts GetItemsOptions) ([]map[string]any, error) {
	if err := c.Login(ctx); err != nil {
		return nil, err
	}
	itemCategories := opts.ItemCategories
	if itemCategories == nil {
		itemCategories = []string{}
	}
	dietCategories := opts.DietCategories
	if dietCategories == nil {
		dietCategories = []string{}
	}
	var searchPhrase any
	if opts.SearchPhrase != "" {
		searchPhrase = opts.SearchPhrase
	}
	var pickupEarliest, pickupLatest any
	if opts.PickupEarliest != nil {
		pickupEarliest = opts.PickupEarliest.Format(time.RFC3339)
	}
	if opts.PickupLatest != nil {
		pickupLatest = opts.PickupLatest.Format(time.RFC3339)
	}
	body := map[string]any{
		"origin":          map[string]any{"latitude": opts.Latitude, "longitude": opts.Longitude},
		"radius":          opts.Radius,
		"page_size":       opts.PageSize,
		"page":            opts.Page,
		"discover":        opts.Discover,
		"favorites_only":  opts.FavoritesOnly,
		"item_categories": itemCategories,
		"diet_categories": dietCategories,
		"pickup_earliest": pickupEarliest,
		"pickup_latest":   pickupLatest,
		"search_phrase":   searchPhrase,
		"with_stock_only": opts.WithStockOnly,
		"hidden_only":     opts.HiddenOnly,
		"we_care_only":    opts.WeCareOnly,
	}
	resp, err := c.post(ctx, c.urlFor(APIItemEndpoint), body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(resp.Body)}
	}
	var out struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(resp.Body), Err: fmt.Errorf("decode get_items response: %w", err)}
	}
	if out.Items == nil {
		out.Items = []map[string]any{}
	}
	return out.Items, nil
}

// GetItem returns a single item by ID.
func (c *Client) GetItem(ctx context.Context, itemID string) (map[string]any, error) {
	if err := c.Login(ctx); err != nil {
		return nil, err
	}
	resp, err := c.post(ctx, c.urlFor(APIItemEndpoint)+escapeID(itemID), map[string]any{"origin": nil})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(resp.Body)}
	}
	var out map[string]any
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(resp.Body), Err: fmt.Errorf("decode get_item response: %w", err)}
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

// GetFavoritesOptions configures GetFavorites.
type GetFavoritesOptions struct {
	// Latitude is the search origin's latitude in degrees.
	Latitude float64
	// Longitude is the search origin's longitude in degrees.
	Longitude float64
	// Radius is the search radius sent to the API.
	Radius int
	// PageSize is the requested number of favorites per page.
	PageSize int
	// Page is the requested page number.
	Page int
}

// DefaultGetFavoritesOptions returns radius=21, page_size=50, page=0.
func DefaultGetFavoritesOptions() GetFavoritesOptions {
	return GetFavoritesOptions{Radius: 21, PageSize: 50, Page: 0}
}

// GetFavorites returns the caller's favorite stores via the discover bucket.
func (c *Client) GetFavorites(ctx context.Context, opts GetFavoritesOptions) ([]map[string]any, error) {
	if err := c.Login(ctx); err != nil {
		return nil, err
	}
	body := map[string]any{
		"origin": map[string]any{"latitude": opts.Latitude, "longitude": opts.Longitude},
		"radius": opts.Radius,
		"paging": map[string]any{"page": opts.Page, "size": opts.PageSize},
		"bucket": map[string]any{"filler_type": "Favorites"},
	}
	resp, err := c.post(ctx, c.urlFor(APIBucketEndpoint), body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(resp.Body)}
	}
	var out struct {
		MobileBucket struct {
			Items []map[string]any `json:"items"`
		} `json:"mobile_bucket"`
	}
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(resp.Body), Err: fmt.Errorf("decode get_favorites response: %w", err)}
	}
	items := out.MobileBucket.Items
	if items == nil {
		items = []map[string]any{}
	}
	return items, nil
}

// SetFavorite adds or removes a store from favorites.
func (c *Client) SetFavorite(ctx context.Context, itemID string, isFavorite bool) error {
	if err := c.Login(ctx); err != nil {
		return err
	}
	resp, err := c.post(ctx, c.urlFor(fmt.Sprintf(FavoriteItemEndpoint, escapeID(itemID))), map[string]any{
		"is_favorite": isFavorite,
	})
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return &APIError{StatusCode: resp.StatusCode, Body: string(resp.Body)}
	}
	return nil
}

// CreateOrder reserves itemCount of itemID. Returns the order object on success.
func (c *Client) CreateOrder(ctx context.Context, itemID string, itemCount int) (map[string]any, error) {
	if err := c.Login(ctx); err != nil {
		return nil, err
	}
	resp, err := c.post(ctx, c.urlFor(CreateOrderEndpoint)+escapeID(itemID), map[string]any{
		"item_count": itemCount,
	})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(resp.Body)}
	}
	var out struct {
		State string         `json:"state"`
		Order map[string]any `json:"order"`
	}
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(resp.Body), Err: fmt.Errorf("decode create_order response: %w", err)}
	}
	if out.State != "SUCCESS" {
		return nil, &APIError{StatusCode: resp.StatusCode, State: out.State, Body: string(resp.Body)}
	}
	if out.Order == nil {
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(resp.Body), Err: errors.New("create_order response is missing an order object")}
	}
	return out.Order, nil
}

// GetOrderStatus returns the status of an order by ID.
func (c *Client) GetOrderStatus(ctx context.Context, orderID string) (map[string]any, error) {
	if err := c.Login(ctx); err != nil {
		return nil, err
	}
	resp, err := c.post(ctx, c.urlFor(fmt.Sprintf(OrderStatusEndpoint, escapeID(orderID))), nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(resp.Body)}
	}
	var out map[string]any
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(resp.Body), Err: fmt.Errorf("decode get_order_status response: %w", err)}
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

// AbortOrder cancels an unpaid order.
func (c *Client) AbortOrder(ctx context.Context, orderID string) error {
	if err := c.Login(ctx); err != nil {
		return err
	}
	resp, err := c.post(ctx, c.urlFor(fmt.Sprintf(AbortOrderEndpoint, escapeID(orderID))), map[string]any{
		"cancel_reason_id": 1,
	})
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return &APIError{StatusCode: resp.StatusCode, Body: string(resp.Body)}
	}
	var out struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return &APIError{StatusCode: resp.StatusCode, Body: string(resp.Body), Err: fmt.Errorf("decode abort_order response: %w", err)}
	}
	if out.State != "SUCCESS" {
		return &APIError{StatusCode: resp.StatusCode, State: out.State, Body: string(resp.Body)}
	}
	return nil
}

// SignupOptions configures account registration with SignupByEmail.
type SignupOptions struct {
	// Email is the address for the new account.
	Email string
	// Name is the account holder's name.
	Name string
	// CountryID is the account country code. An empty value defaults to GB.
	CountryID string
	// NewsletterOptIn subscribes the account to newsletters.
	NewsletterOptIn bool
	// PushNotificationOptIn enables push notifications for the account.
	PushNotificationOptIn bool
}

// DefaultSignupOptions returns CountryID=GB, PushNotificationOptIn=true.
func DefaultSignupOptions(email string) SignupOptions {
	return SignupOptions{Email: email, CountryID: "GB", PushNotificationOptIn: true}
}

// SignupByEmail registers a new account and stores the resulting tokens on the client.
func (c *Client) SignupByEmail(ctx context.Context, opts SignupOptions) error {
	if opts.CountryID == "" {
		opts.CountryID = "GB"
	}
	resp, err := c.postAuth(ctx, c.urlFor(SignupByEmailEndpoint), map[string]any{
		"country_id":               opts.CountryID,
		"device_type":              c.DeviceType,
		"email":                    opts.Email,
		"name":                     opts.Name,
		"newsletter_opt_in":        opts.NewsletterOptIn,
		"push_notification_opt_in": opts.PushNotificationOptIn,
	})
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return &APIError{StatusCode: resp.StatusCode, Body: string(resp.Body)}
	}
	var out struct {
		LoginResponse authTokens `json:"login_response"`
	}
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return &APIError{StatusCode: resp.StatusCode, Body: string(resp.Body), Err: fmt.Errorf("decode signup response: %w", err)}
	}
	if err := out.LoginResponse.validate(); err != nil {
		return &APIError{StatusCode: resp.StatusCode, Body: string(resp.Body), Err: fmt.Errorf("validate signup response: %w", err)}
	}
	c.storeTokens(out.LoginResponse, resp)
	return nil
}

// GetActive returns the active orders.
func (c *Client) GetActive(ctx context.Context) (map[string]any, error) {
	if err := c.Login(ctx); err != nil {
		return nil, err
	}
	resp, err := c.post(ctx, c.urlFor(ActiveOrderEndpoint), map[string]any{})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(resp.Body)}
	}
	var out map[string]any
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(resp.Body), Err: fmt.Errorf("decode get_active response: %w", err)}
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

// GetInactiveOptions configures GetInactive.
type GetInactiveOptions struct {
	// Page is the requested page number.
	Page int
	// PageSize is the requested number of inactive orders per page.
	PageSize int
}

// DefaultGetInactiveOptions returns page=0, page_size=20.
func DefaultGetInactiveOptions() GetInactiveOptions {
	return GetInactiveOptions{Page: 0, PageSize: 20}
}

// GetInactive returns inactive orders, paginated.
func (c *Client) GetInactive(ctx context.Context, opts GetInactiveOptions) (map[string]any, error) {
	if err := c.Login(ctx); err != nil {
		return nil, err
	}
	resp, err := c.post(ctx, c.urlFor(InactiveOrderEndpoint), map[string]any{
		"paging": map[string]any{"page": opts.Page, "size": opts.PageSize},
	})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(resp.Body)}
	}
	var out map[string]any
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(resp.Body), Err: fmt.Errorf("decode get_inactive response: %w", err)}
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

// GetManufacturerItems returns delivery items.
func (c *Client) GetManufacturerItems(ctx context.Context) (map[string]any, error) {
	if err := c.Login(ctx); err != nil {
		return nil, err
	}
	body := map[string]any{
		"display_types_accepted": []string{"LIST"},
		"element_types_accepted": []string{
			"ITEM", "NPS", "TEXT", "DUO_ITEMS", "MANUFACTURER_STORY_CARD",
		},
		"action_types_accepted": []string{},
	}
	resp, err := c.post(ctx, c.urlFor(ManufacturerItemEndpoint), body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(resp.Body)}
	}
	var out map[string]any
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(resp.Body), Err: fmt.Errorf("decode get_manufacturer_items response: %w", err)}
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

// stdinPinReader prompts on out and reads a line from in.
func stdinPinReader(out io.Writer, in io.Reader) (string, error) {
	fmt.Fprint(out, "Enter PIN from email: ")
	r := bufio.NewReader(in)
	line, err := r.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return line, nil
}

func generateDataDomeCID() string {
	var out strings.Builder
	out.Grow(120)
	for i := 0; i < 120; i++ {
		out.WriteByte(dataDomeCIDChars[mathrand.IntN(len(dataDomeCIDChars))])
	}
	return out.String()
}

// newUUID returns a RFC 4122 v4 UUID using crypto/rand.
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fall back to a non-crypto random number; correlation IDs need not be
		// cryptographically strong.
		for i := range b {
			b[i] = byte(mathrand.UintN(256))
		}
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	hex := func(buf []byte) string {
		const digits = "0123456789abcdef"
		out := make([]byte, len(buf)*2)
		for i, x := range buf {
			out[2*i] = digits[x>>4]
			out[2*i+1] = digits[x&0x0f]
		}
		return string(out)
	}
	return strings.Join([]string{hex(b[0:4]), hex(b[4:6]), hex(b[6:8]), hex(b[8:10]), hex(b[10:16])}, "-")
}
