// Package tgtg is an unofficial Go client for the TooGoodToGo API.
package tgtg

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	mathrand "math/rand/v2"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	BaseURL                  = "https://apptoogoodtogo.com/api/"
	APIItemEndpoint          = "item/v8/"
	FavoriteItemEndpoint     = "user/favorite/v1/%s/update"
	AuthByEmailEndpoint      = "auth/v5/authByEmail"
	AuthPollingEndpoint      = "auth/v5/authByRequestPollingId"
	AuthByRequestPinEndpoint = "auth/v5/authByRequestPin"
	SignupByEmailEndpoint    = "auth/v5/signUpByEmail"
	RefreshEndpoint          = "token/v1/refresh"
	ActiveOrderEndpoint      = "order/v8/active"
	InactiveOrderEndpoint    = "order/v8/inactive"
	CreateOrderEndpoint      = "order/v8/create/"
	AbortOrderEndpoint       = "order/v8/%s/abort"
	OrderStatusEndpoint      = "order/v8/%s/status"
	APIBucketEndpoint        = "discover/v1/bucket"
	ManufacturerItemEndpoint = "manufactureritem/v2"
	DataDomeSDKURL           = "https://api-sdk.datadome.co/sdk/"

	DefaultAPKVersion          = "24.11.0"
	DefaultAccessTokenLifetime = 4 * time.Hour
	MaxPollingTries            = 24
	PollingWaitTime            = 5 * time.Second
)

var userAgentTemplates = []string{
	"TGTG/%s Dalvik/2.1.0 (Linux; U; Android 9; Nexus 5 Build/M4B30Z)",
	"TGTG/%s Dalvik/2.1.0 (Linux; U; Android 10; SM-G935F Build/NRD90M)",
	"TGTG/%s Dalvik/2.1.0 (Linux; Android 12; SM-G920V Build/MMB29K)",
}

var reDataDomeCookie = regexp.MustCompile(`datadome=([^;]+)`)

// httpResponse is the value-type result of a successful POST. Returning it by
// value (rather than *http.Response) makes the contract that "no error => valid
// fields" enforced by the type system.
type httpResponse struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

// collectSetCookie returns all Set-Cookie headers joined into a single value
// suitable for round-tripping. The cookiejar handles per-request cookie
// matching; this string exists so callers can persist credentials and
// reconstruct a Client later.
func collectSetCookie(h http.Header) string {
	values := h.Values("Set-Cookie")
	if len(values) == 0 {
		return ""
	}
	return strings.Join(values, ", ")
}

const dataDomeCIDChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789~_"

// Credentials holds the tokens returned by GetCredentials.
type Credentials struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Cookie       string `json:"cookie"`
}

// Config configures a Client. All fields are optional.
type Config struct {
	URL                    string
	Email                  string
	AccessToken            string
	RefreshToken           string
	Cookie                 string
	UserAgent              string
	Language               string
	DeviceType             string
	APKVersion             string
	AccessTokenLifetime    time.Duration
	LastTimeTokenRefreshed time.Time
	Timeout                time.Duration
	HTTPClient             *http.Client
	DataDomeSDKURL         string
	// PinReader returns the PIN entered by the user during email login. If it
	// returns an empty string, the client falls back to the legacy polling flow.
	PinReader func() (string, error)
	// Now and Sleep are exposed for testability. Defaults: time.Now / time.Sleep.
	Now   func() time.Time
	Sleep func(time.Duration)
	// Output controls where login progress messages are written. Defaults to os.Stdout.
	Output io.Writer
	// APKVersionFetcher fetches the latest TooGoodToGo APK version. It is
	// invoked lazily on the first request when neither UserAgent nor APKVersion
	// is supplied, and receives the request's context. Defaults to
	// GetLastAPKVersion.
	APKVersionFetcher func(ctx context.Context) (string, error)
}

// Client talks to the TooGoodToGo API.
type Client struct {
	BaseURL                string
	Email                  string
	AccessToken            string
	RefreshToken           string
	Cookie                 string
	UserAgent              string
	Language               string
	DeviceType             string
	APKVersion             string
	AccessTokenLifetime    time.Duration
	LastTimeTokenRefreshed time.Time
	Timeout                time.Duration

	correlationID     string
	httpClient        *http.Client
	dataDomeSDKURL    string
	pinReader         func() (string, error)
	now               func() time.Time
	sleep             func(time.Duration)
	out               io.Writer
	apkVersionFetcher func(ctx context.Context) (string, error)
}

// New creates a Client using cfg, applying defaults for unset fields.
func New(cfg Config) *Client {
	if cfg.URL == "" {
		cfg.URL = BaseURL
	}
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
	if cfg.Sleep == nil {
		cfg.Sleep = time.Sleep
	}
	if cfg.Output == nil {
		cfg.Output = os.Stdout
	}
	if cfg.APKVersionFetcher == nil {
		cfg.APKVersionFetcher = GetLastAPKVersion
	}
	if cfg.PinReader == nil {
		out := cfg.Output
		cfg.PinReader = func() (string, error) {
			return stdinPinReader(out, os.Stdin)
		}
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: cfg.Timeout}
	}
	if httpClient.Jar == nil {
		jar, _ := cookiejar.New(nil)
		httpClient.Jar = jar
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
		now:               cfg.Now,
		sleep:             cfg.Sleep,
		out:               cfg.Output,
		apkVersionFetcher: cfg.APKVersionFetcher,
	}

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
func (c *Client) resolveUserAgent(ctx context.Context) {
	if c.UserAgent != "" {
		return
	}
	if c.APKVersion == "" {
		v, err := c.apkVersionFetcher(ctx)
		if err != nil {
			c.APKVersion = DefaultAPKVersion
			fmt.Fprintln(c.out, "Failed to get last version")
		} else {
			c.APKVersion = v
		}
	}
	fmt.Fprintf(c.out, "Using version %s\n", c.APKVersion)
	tmpl := userAgentTemplates[mathrand.IntN(len(userAgentTemplates))]
	c.UserAgent = fmt.Sprintf(tmpl, c.APKVersion)
}

func (c *Client) buildHeaders() http.Header {
	h := http.Header{}
	h.Set("Accept", "application/json")
	h.Set("Accept-Encoding", "gzip")
	h.Set("Accept-Language", c.Language)
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("User-Agent", c.UserAgent)
	h.Set("X-Correlation-ID", c.correlationID)
	if c.Cookie != "" {
		h.Set("Cookie", c.Cookie)
	}
	if c.AccessToken != "" {
		h.Set("Authorization", "Bearer "+c.AccessToken)
	}
	return h
}

func (c *Client) alreadyLogged() bool {
	return c.AccessToken != "" && c.RefreshToken != ""
}

func (c *Client) urlFor(path string) string {
	return c.BaseURL + path
}

// post sends a POST with JSON body, ensuring a DataDome cookie is attempted
// and retrying once with a fresh cookie on a 403.
func (c *Client) post(ctx context.Context, requestURL string, body any) (httpResponse, error) {
	c.resolveUserAgent(ctx)
	c.ensureDataDomeCookie(ctx, requestURL)
	res, err := c.doPost(ctx, requestURL, body)
	if err != nil {
		return httpResponse{}, err
	}
	if res.StatusCode == http.StatusForbidden {
		c.clearCookies()
		c.fetchDataDomeCookie(ctx, requestURL)
		res, err = c.doPost(ctx, requestURL, body)
		if err != nil {
			return httpResponse{}, err
		}
	}
	return res, nil
}

func (c *Client) doPost(ctx context.Context, requestURL string, body any) (httpResponse, error) {
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
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return httpResponse{}, err
	}
	defer resp.Body.Close()
	var bodyReader io.Reader = resp.Body
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			return httpResponse{}, fmt.Errorf("gzip reader: %w", err)
		}
		defer gz.Close()
		bodyReader = gz
	}
	payload, err := io.ReadAll(bodyReader)
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

func (c *Client) clearCookies() {
	jar, _ := cookiejar.New(nil)
	c.httpClient.Jar = jar
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
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		fmt.Fprintln(c.out, "Failed to fetch DataDome cookie")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	body, err := io.ReadAll(resp.Body)
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
	m := reDataDomeCookie.FindStringSubmatch(data.Cookie)
	if m == nil {
		return
	}
	apiURL, err := url.Parse(c.BaseURL)
	if err != nil {
		return
	}
	c.httpClient.Jar.SetCookies(apiURL, []*http.Cookie{{
		Name:   "datadome",
		Value:  m[1],
		Domain: apiURL.Hostname(),
		Path:   "/",
		Secure: true,
	}})
}

// Login authenticates the client. The first call requires either an email or a
// full set of access token, refresh token and cookie. If access and refresh
// tokens are already present, Login refreshes the access token (when expired);
// otherwise it kicks off the email login flow.
func (c *Client) Login(ctx context.Context) error {
	if c.Email == "" && !(c.AccessToken != "" && c.RefreshToken != "" && c.Cookie != "") {
		return errors.New("you must provide at least email or access_token, refresh_token and cookie")
	}
	if c.alreadyLogged() {
		return c.refreshToken(ctx)
	}
	resp, err := c.post(ctx, c.urlFor(AuthByEmailEndpoint), map[string]any{
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
			return &LoginError{StatusCode: resp.StatusCode, Body: string(resp.Body)}
		}
		switch first.State {
		case "TERMS":
			return &PollingError{Message: fmt.Sprintf(
				"This email %s is not linked to a tgtg account. Please signup with this email first.", c.Email)}
		case "WAIT":
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
	resp, err := c.post(ctx, c.urlFor(RefreshEndpoint), map[string]any{
		"refresh_token": c.RefreshToken,
	})
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return &APIError{StatusCode: resp.StatusCode, Body: string(resp.Body)}
	}
	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(resp.Body, &tok); err != nil {
		return &APIError{StatusCode: resp.StatusCode, Body: string(resp.Body)}
	}
	c.AccessToken = tok.AccessToken
	c.RefreshToken = tok.RefreshToken
	c.LastTimeTokenRefreshed = c.now()
	c.Cookie = collectSetCookie(resp.Header)
	return nil
}

func (c *Client) startPolling(ctx context.Context, pollingID string) error {
	fmt.Fprintln(c.out, "Check your email for a login PIN code.")
	pin, err := c.pinReader()
	if err != nil {
		return err
	}
	pin = strings.TrimSpace(pin)
	if pin != "" {
		return c.authByPIN(ctx, pollingID, pin)
	}
	for i := 0; i < MaxPollingTries; i++ {
		resp, err := c.post(ctx, c.urlFor(AuthPollingEndpoint), map[string]any{
			"device_type":        c.DeviceType,
			"email":              c.Email,
			"request_polling_id": pollingID,
		})
		if err != nil {
			return err
		}
		switch resp.StatusCode {
		case http.StatusAccepted:
			fmt.Fprintln(c.out, "Check your mailbox on PC to continue... "+
				"(Opening email on mobile won't work, if you have installed tgtg app.)")
			c.sleep(PollingWaitTime)
			continue
		case http.StatusOK:
			fmt.Fprintln(c.out, "Logged in!")
			var tok struct {
				AccessToken  string `json:"access_token"`
				RefreshToken string `json:"refresh_token"`
			}
			if err := json.Unmarshal(resp.Body, &tok); err != nil {
				return &LoginError{StatusCode: resp.StatusCode, Body: string(resp.Body)}
			}
			c.AccessToken = tok.AccessToken
			c.RefreshToken = tok.RefreshToken
			c.LastTimeTokenRefreshed = c.now()
			c.Cookie = collectSetCookie(resp.Header)
			return nil
		case http.StatusTooManyRequests:
			return &APIError{StatusCode: resp.StatusCode, Body: "Too many requests. Try again later."}
		default:
			return &LoginError{StatusCode: resp.StatusCode, Body: string(resp.Body)}
		}
	}
	return &PollingError{Message: fmt.Sprintf(
		"Max retries (%d seconds) reached. Try again.", int(MaxPollingTries*PollingWaitTime/time.Second))}
}

func (c *Client) authByPIN(ctx context.Context, pollingID, pin string) error {
	resp, err := c.post(ctx, c.urlFor(AuthByRequestPinEndpoint), map[string]any{
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
	fmt.Fprintln(c.out, "Logged in!")
	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(resp.Body, &tok); err != nil {
		return &LoginError{StatusCode: resp.StatusCode, Body: string(resp.Body)}
	}
	c.AccessToken = tok.AccessToken
	c.RefreshToken = tok.RefreshToken
	c.LastTimeTokenRefreshed = c.now()
	c.Cookie = collectSetCookie(resp.Header)
	return nil
}

// GetCredentials logs in (if needed) and returns the current tokens.
func (c *Client) GetCredentials(ctx context.Context) (Credentials, error) {
	if err := c.Login(ctx); err != nil {
		return Credentials{}, err
	}
	return Credentials{
		AccessToken:  c.AccessToken,
		RefreshToken: c.RefreshToken,
		Cookie:       c.Cookie,
	}, nil
}

// GetItemsOptions mirrors the keyword arguments of Python's get_items.
type GetItemsOptions struct {
	Latitude       float64
	Longitude      float64
	Radius         int
	PageSize       int
	Page           int
	Discover       bool
	FavoritesOnly  bool
	ItemCategories []string
	DietCategories []string
	PickupEarliest *time.Time
	PickupLatest   *time.Time
	SearchPhrase   string
	WithStockOnly  bool
	HiddenOnly     bool
	WeCareOnly     bool
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

// GetItems lists items, defaulting to the caller's favorites.
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
		return nil, fmt.Errorf("decode get_items response: %w", err)
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
	resp, err := c.post(ctx, c.urlFor(APIItemEndpoint)+itemID, map[string]any{"origin": nil})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(resp.Body)}
	}
	var out map[string]any
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return nil, fmt.Errorf("decode get_item response: %w", err)
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

// GetFavoritesOptions configures GetFavorites.
type GetFavoritesOptions struct {
	Latitude  float64
	Longitude float64
	Radius    int
	PageSize  int
	Page      int
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
		return nil, fmt.Errorf("decode get_favorites response: %w", err)
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
	resp, err := c.post(ctx, c.urlFor(fmt.Sprintf(FavoriteItemEndpoint, itemID)), map[string]any{
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
	resp, err := c.post(ctx, c.urlFor(CreateOrderEndpoint)+itemID, map[string]any{
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
		return nil, fmt.Errorf("decode create_order response: %w", err)
	}
	if out.State != "SUCCESS" {
		return nil, &APIError{State: out.State, Body: string(resp.Body)}
	}
	if out.Order == nil {
		out.Order = map[string]any{}
	}
	return out.Order, nil
}

// GetOrderStatus returns the status of an order by ID.
func (c *Client) GetOrderStatus(ctx context.Context, orderID string) (map[string]any, error) {
	if err := c.Login(ctx); err != nil {
		return nil, err
	}
	resp, err := c.post(ctx, c.urlFor(fmt.Sprintf(OrderStatusEndpoint, orderID)), nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(resp.Body)}
	}
	var out map[string]any
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return nil, fmt.Errorf("decode get_order_status response: %w", err)
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
	resp, err := c.post(ctx, c.urlFor(fmt.Sprintf(AbortOrderEndpoint, orderID)), map[string]any{
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
		return &APIError{StatusCode: resp.StatusCode, Body: string(resp.Body)}
	}
	if out.State != "SUCCESS" {
		return &APIError{State: out.State, Body: string(resp.Body)}
	}
	return nil
}

// SignupOptions mirrors signup_by_email keyword arguments.
type SignupOptions struct {
	Email                 string
	Name                  string
	CountryID             string
	NewsletterOptIn       bool
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
	resp, err := c.post(ctx, c.urlFor(SignupByEmailEndpoint), map[string]any{
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
		LoginResponse struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
		} `json:"login_response"`
	}
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return &APIError{StatusCode: resp.StatusCode, Body: string(resp.Body)}
	}
	c.AccessToken = out.LoginResponse.AccessToken
	c.RefreshToken = out.LoginResponse.RefreshToken
	c.LastTimeTokenRefreshed = c.now()
	c.Cookie = collectSetCookie(resp.Header)
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
		return nil, fmt.Errorf("decode get_active response: %w", err)
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

// GetInactiveOptions configures GetInactive.
type GetInactiveOptions struct {
	Page     int
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
		return nil, fmt.Errorf("decode get_inactive response: %w", err)
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
		return nil, fmt.Errorf("decode get_manufacturer_items response: %w", err)
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
