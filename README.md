# tgtg-go

[![Go Reference](https://pkg.go.dev/badge/github.com/mikispag/tgtg-go.svg)](https://pkg.go.dev/github.com/mikispag/tgtg-go)
[![CI](https://github.com/mikispag/tgtg-go/actions/workflows/ci.yml/badge.svg)](https://github.com/mikispag/tgtg-go/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/mikispag/tgtg-go)](https://goreportcard.com/report/github.com/mikispag/tgtg-go)
[![Go Version](https://img.shields.io/github/go-mod/go-version/mikispag/tgtg-go)](go.mod)
[![License: GPL-3.0](https://img.shields.io/badge/License-GPL--3.0-blue.svg)](LICENSE)

An unofficial, robust, and idiomatic Go client for the [TooGoodToGo](https://toogoodtogo.com) API.

Originally ported from [`tgtg-python`](https://github.com/ahivert/tgtg-python), with native `context.Context` support, typed errors, transparent DataDome cookie management, and standard-library-only dependencies.

---

## Table of Contents

- [Features](#features)
- [Installation](#installation)
- [Authentication](#authentication)
  - [Email Login with PIN](#1-email-login-with-pin)
  - [Using Existing Tokens](#2-using-existing-tokens)
- [Usage](#usage)
  - [Browse Items & Favorites](#browse-items--favorites)
  - [Get Single Item](#get-single-item)
  - [Delivery / Manufacturer Items](#delivery--manufacturer-items)
  - [Manage Favorites](#manage-favorites)
  - [Orders Lifecycle](#orders-lifecycle)
  - [Sign Up](#sign-up)
- [Error Handling](#error-handling)
- [DataDome Bot Protection](#datadome-bot-protection)
- [Development](#development)
- [License](#license)

---

## Features

- **Full API Coverage**: Authentication, store discovery, favorites, orders, and delivery items.
- **DataDome Protection Bypass**: Automatic device fingerprinting, cookie management, and 403 retries.
- **Context-Aware**: Full support for `context.Context` cancellation and timeouts across all requests.
- **Typed Errors**: Structured errors (`*LoginError`, `*APIError`, `*PollingError`) inspectable via `errors.As`.
- **Zero External Dependencies**: Built entirely on the Go standard library.
- **Testable & Pluggable**: Custom PIN readers, clocks, and HTTP clients for automated and headless use.

---

## Installation

Requires **Go 1.23+**.

```bash
go get github.com/mikispag/tgtg-go
```

---

## Authentication

### 1. Email Login with PIN

Provide your account email to receive a login PIN:

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/mikispag/tgtg-go"
)

func main() {
	ctx := context.Background()
	client := tgtg.New(tgtg.Config{
		Email: "user@example.com",
	})

	// Prompts on stdin for the PIN sent to your email
	creds, err := client.GetCredentials(ctx)
	if err != nil {
		log.Fatalf("Login failed: %v", err)
	}

	fmt.Printf("Access Token:  %s\n", creds.AccessToken)
	fmt.Printf("Refresh Token: %s\n", creds.RefreshToken)
	fmt.Printf("Cookie:        %s\n", creds.Cookie)
}
```

> [!TIP]
> In automated or headless environments, supply a custom `Config.PinReader` callback (e.g. from an API, SMS, or Slack prompt) instead of reading from standard input. Submitting an empty PIN falls back to the link-click polling flow.

### 2. Using Existing Tokens

Once credentials are saved, initialize the client directly:

```go
client := tgtg.New(tgtg.Config{
	AccessToken:  "<your_access_token>",
	RefreshToken: "<your_refresh_token>",
	Cookie:       "<your_cookie>",
})
```

The client automatically refreshes the access token when it nears expiration.

---

## Usage

### Browse Items & Favorites

```go
ctx := context.Background()

// 1. Fetch favorite stores (default)
favorites, err := client.GetItems(ctx, tgtg.DefaultGetItemsOptions())

// 2. Discover stores by location
opts := tgtg.DefaultGetItemsOptions()
opts.FavoritesOnly = false
opts.Latitude = 47.3769
opts.Longitude = 8.5417
opts.Radius = 10 // km
opts.WithStockOnly = true

items, err := client.GetItems(ctx, opts)
```

<details>
<summary>Example items response</summary>

```json
[
  {
    "item": {
      "item_id": "64346",
      "item_price": {"code": "EUR", "minor_units": 499, "decimals": 2},
      "name": "Surprise Bag",
      "description": "Delicious baked goods and sandwiches...",
      "item_category": "BAKED_GOODS",
      "favorite_count": 12,
      "buffet": false
    },
    "store": {
      "store_id": "59949",
      "store_name": "Bakery Zurich Central",
      "store_time_zone": "Europe/Zurich"
    },
    "display_name": "Bakery Zurich Central - Surprise Bag",
    "items_available": 3,
    "distance": 1.2,
    "favorite": true,
    "in_sales_window": true
  }
]
```

</details>

### Get Single Item

```go
item, err := client.GetItem(ctx, "614318")
```

### Delivery / Manufacturer Items

```go
deliveryItems, err := client.GetManufacturerItems(ctx)
```

### Manage Favorites

```go
// List favorites via the discover bucket
favs, err := client.GetFavorites(ctx, tgtg.DefaultGetFavoritesOptions())

// Add or remove a favorite item
err = client.SetFavorite(ctx, "64346", true)  // Favorite
err = client.SetFavorite(ctx, "64346", false) // Unfavorite
```

### Orders Lifecycle

```go
// 1. Create a reservation
order, err := client.CreateOrder(ctx, "614318", 1)

// 2. Check order status
status, err := client.GetOrderStatus(ctx, orderID)

// 3. List active orders
active, err := client.GetActive(ctx)

// 4. List inactive/historical orders
inactive, err := client.GetInactive(ctx, tgtg.DefaultGetInactiveOptions())

// 5. Abort an unpaid reservation
err = client.AbortOrder(ctx, orderID)
```

> [!NOTE]
> Payment cannot be completed through the unofficial API and must be finalized within the official app.

### Sign Up

```go
client := tgtg.New(tgtg.Config{})
err := client.SignupByEmail(ctx, tgtg.DefaultSignupOptions("new_user@example.com"))
```

---

## Error Handling

Errors returned by `tgtg-go` are strongly typed. Inspect them using `errors.As`:

```go
var (
	loginErr   *tgtg.LoginError
	apiErr     *tgtg.APIError
	pollingErr *tgtg.PollingError
)

switch {
case errors.As(err, &loginErr):
	fmt.Printf("Login failed (HTTP %d): %s\n", loginErr.StatusCode, loginErr.Body)
case errors.As(err, &apiErr):
	fmt.Printf("API returned error (HTTP %d, State %s): %s\n", apiErr.StatusCode, apiErr.State, apiErr.Body)
case errors.As(err, &pollingErr):
	fmt.Printf("Polling error: %s\n", pollingErr.Message)
}
```

| Type | Description |
| :--- | :--- |
| [`*LoginError`](file:///home/miki/go/src/github.com/mikispag/tgtg-go/errors.go#L7) | Authentication failure or invalid credentials. |
| [`*APIError`](file:///home/miki/go/src/github.com/mikispag/tgtg-go/errors.go#L19) | Non-2xx response or non-`SUCCESS` order state. |
| [`*PollingError`](file:///home/miki/go/src/github.com/mikispag/tgtg-go/errors.go#L13) | Unregistered email or polling retry timeout. |

---

## DataDome Bot Protection

TooGoodToGo uses [DataDome](https://datadome.co/) bot protection.

- The client transparently requests and attaches a valid `datadome` cookie using device fingerprint parameters matching the Android app.
- If a request receives an HTTP 403, the client clears cookies, re-fetches a fresh DataDome token, and retries the request automatically.
- **Tip**: Residential IP addresses have the highest success rate; datacenter/VPN IPs are often blocked by DataDome upstream.

---

## Development

```bash
# Build
make build

# Run all tests with race detector
make test

# Format and lint
make fmt
make lint
```

---

## License

This project is licensed under the **GPL-3.0 License**. See [LICENSE](LICENSE) for details.
