[![Go Reference](https://pkg.go.dev/badge/github.com/mikispag/tgtg-go.svg)](https://pkg.go.dev/github.com/mikispag/tgtg-go)
[![Go Version](https://img.shields.io/github/go-mod/go-version/mikispag/tgtg-go)](go.mod)
[![License](https://img.shields.io/badge/license-GPL--3.0-blue.svg)](LICENSE)

# tgtg-go

> Unofficial Go client for the [TooGoodToGo](https://toogoodtogo.com) API.
> Ported from [`tgtg-python`](https://github.com/ahivert/tgtg-python).

Go version: **1.23+**

Handles:

- create an account (`/api/auth/vX/signUpByEmail`)
- login (`/api/auth/vX/authByEmail`)
- refresh token (`token/v1/refresh`)
- list stores (`/api/item/vX`)
- get a store (`/api/item/vX/:id`)
- get favorites (`/api/discover/vX/bucket`)
- set favorite (`/api/user/favorite/vX/:id/update`)
- create an order (`/api/order/vX/create/:id`)
- abort an order (`/api/order/vX/:id/abort`)
- get the status of an order (`/api/order/vX/:id/status`)
- get active orders (`/api/order/vX/active`)
- get inactive orders (`/api/order/vX/inactive`)
- get delivery items (`api/manufactureritem/v2`)

## Install

```bash
go get github.com/mikispag/tgtg-go
```

## Use it

### Retrieve tokens

Build the client with your email:

```go
import "github.com/mikispag/tgtg-go"

client := tgtg.New(tgtg.Config{Email: "<your_email>"})
creds, err := client.GetCredentials(context.Background())
```

You should receive an email from tgtg. By default, the client prompts on stdin
for the PIN code in the email; supply your own callback via `Config.PinReader`
to integrate with another input source. Submitting an empty PIN falls back to
the legacy link-click polling flow.

Once authenticated:

```go
fmt.Printf("%+v\n", creds)
// {AccessToken:<your_access_token> RefreshToken:<your_refresh_token> Cookie:<cookie>}
```

### Build the client from tokens

```go
client := tgtg.New(tgtg.Config{
    AccessToken:  "<access_token>",
    RefreshToken: "<refresh_token>",
    Cookie:       "<cookie>",
})
```

### Get items

```go
ctx := context.Background()

// By default it will *only* get your favorites.
items, err := client.GetItems(ctx, tgtg.DefaultGetItemsOptions())

// To get items (not only your favorites) you need to provide location info.
opts := tgtg.DefaultGetItemsOptions()
opts.FavoritesOnly = false
opts.Latitude = 48.126
opts.Longitude = -1.723
opts.Radius = 10
items, err = client.GetItems(ctx, opts)
```

<details>
<summary>Example response</summary>

```json
[
  {
    "item": {
      "item_id": "64346",
      "item_price": {"code": "EUR", "minor_units": 499, "decimals": 2},
      "name": "",
      "description": "Salva comida en Ecofamily Bufé...",
      "item_category": "MEAL",
      "favorite_count": 0,
      "buffet": false
    },
    "store": {
      "store_id": "59949s",
      "store_name": "Ecofamily Bufé - Centro",
      "store_time_zone": "Europe/Madrid"
    },
    "display_name": "Ecofamily Bufé - Centro",
    "items_available": 0,
    "distance": 4241.99,
    "favorite": true,
    "in_sales_window": false,
    "new_item": false
  }
]
```

</details>

### Get an item

```go
item, err := client.GetItem(ctx, "614318")
```

<details>
<summary>Example response</summary>

```json
{
  "item": {
    "item_id": "614318",
    "name": "Panier petit déjeuner",
    "item_category": "BAKED_GOODS",
    "buffet": true
  },
  "store": {
    "store_id": "624740",
    "store_name": "Hôtel Les Matins de Paris & Spa",
    "store_time_zone": "Europe/Paris"
  },
  "display_name": "Hôtel Les Matins de Paris & Spa (Panier petit déjeuner)",
  "pickup_interval": {"start": "2022-11-04T11:00:00Z", "end": "2022-11-04T15:00:00Z"},
  "items_available": 0,
  "favorite": true,
  "in_sales_window": true
}
```

</details>

Responses are returned as `map[string]any` to mirror the pass-through behavior
of the Python client.

### Create an order

```go
order, err := client.CreateOrder(ctx, itemID, numberOfItemsToOrder)
```

<details>
<summary>Example response</summary>

```json
{
  "id": "<order_id>",
  "item_id": "<item_id_that_was_ordered>",
  "state": "RESERVED",
  "order_line": {
    "quantity": 1,
    "item_price_including_taxes": {"code": "EUR", "minor_units": 600, "decimals": 2}
  },
  "reserved_at": "2023-01-01T10:30:32.331280392",
  "order_type": "MAGICBAG"
}
```

</details>

> [!NOTE]
> Payment of an order is currently not implemented. You can create an order via
> this client, but you cannot pay for it.

### Get the status of an order

```go
status, err := client.GetOrderStatus(ctx, orderID)
```

<details>
<summary>Example response</summary>

```json
{"id": "<order_id>", "item_id": "<item_id_that_was_ordered>", "state": "RESERVED"}
```

</details>

### Abort an order

```go
err := client.AbortOrder(ctx, orderID)
```

Returns no value on success. The app uses this call when the user aborts an
order *before* paying. After payment, a different call is used.

### Get active orders

```go
active, err := client.GetActive(ctx)
```

### Get inactive orders

```go
res, err := client.GetInactive(ctx, tgtg.DefaultGetInactiveOptions())
// res["has_more"] is true if more results are available.
```

To e.g. sum up all orders you have ever made:

```go
import "math"

var orders []map[string]any
page := 0
for {
    res, err := client.GetInactive(ctx, tgtg.GetInactiveOptions{Page: page, PageSize: 200})
    if err != nil { log.Fatal(err) }
    chunk, _ := res["orders"].([]any)
    for _, o := range chunk {
        orders = append(orders, o.(map[string]any))
    }
    if hasMore, _ := res["has_more"].(bool); !hasMore { break }
    page++
}

var redeemed []map[string]any
var redeemedItems int
var moneySpent float64
for _, o := range orders {
    if o["state"] != "REDEEMED" { continue }
    redeemed = append(redeemed, o)
    qty, _ := o["quantity"].(float64)
    redeemedItems += int(qty)
    p := o["price_including_taxes"].(map[string]any)
    moneySpent += p["minor_units"].(float64) / math.Pow(10, p["decimals"].(float64))
}

fmt.Printf("Total orders: %d\n", len(orders))
fmt.Printf("Total picked up: %d\n", len(redeemed))
fmt.Printf("Total items picked up: %d\n", redeemedItems)
fmt.Printf("Total money spent: ~%.2f\n", moneySpent)
```

### Get favorites

This lists all currently favorited stores. Behavior mirrors `GetItems` but
better matches the official app:

```go
favorites, err := client.GetFavorites(ctx, tgtg.DefaultGetFavoritesOptions())
```

### Set favorite

```go
// add favorite
err := client.SetFavorite(ctx, "64346", true)

// remove favorite
err = client.SetFavorite(ctx, "64346", false)
```

### Create an account

```go
client := tgtg.New(tgtg.Config{})
err := client.SignupByEmail(ctx, tgtg.DefaultSignupOptions("<your_email>"))
// client is now ready to be used
```

## Errors

All errors are typed — use `errors.As` to match:

| Error             | When                                                       |
| ----------------- | ---------------------------------------------------------- |
| `*LoginError`     | login failure (bad credentials, unknown auth state)        |
| `*APIError`       | non-2xx response, or `state != "SUCCESS"` on order calls   |
| `*PollingError`   | email not registered, or polling exhausted without success |

## DataDome

TooGoodToGo uses DataDome for bot protection. The client transparently fetches
a `datadome` cookie before each request and retries once with a fresh cookie on
HTTP 403. VPN/datacenter IPs are frequently blocked regardless of cookie
validity — residential IPs work best.

## Developers

```bash
make build    # go build ./...
make test     # go test -race ./...
make vet      # go vet ./...
make lint     # vet + gofmt check
```

## License

GPL-3.0 — see [`LICENSE`](LICENSE).
