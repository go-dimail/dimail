<p align="center"><img src="https://raw.githubusercontent.com/go-dimail/brand/main/social.png" alt="go-dimail" width="720"></p>

# go-dimail

[![ci](https://github.com/go-dimail/dimail/actions/workflows/ci.yml/badge.svg)](https://github.com/go-dimail/dimail/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/go-dimail/dimail.svg)](https://pkg.go.dev/github.com/go-dimail/dimail)
[![Go Report Card](https://goreportcard.com/badge/github.com/go-dimail/dimail)](https://goreportcard.com/report/github.com/go-dimail/dimail)

A pure-Go (CGO-free) client for the **Dimail API** — the mail-hosting
management API of the French government's *La Suite numérique* platform
(<https://api.osprod.dimail1.numerique.gouv.fr>).

The typed models and the request methods are **generated from the API's own
OpenAPI document** (`openapi.json`, committed at the repository root) by the
generator in [`internal/gen`](internal/gen). Everything the API exposes — 91
operations across users, domains, mailboxes (v1 and v2), aliases, forwards,
allows, identities, app-passwords, the `/my` self-service views, and the
`/system` and `/logs` administration surface — is available with concrete Go
types. Regenerate after refreshing the spec with:

```sh
go generate ./...
```

- **CGO-free** — the same test binary runs identically on all six supported
  64-bit targets (`amd64`, `arm64`, `riscv64`, `loong64`, `ppc64le`, `s390x`).
- **100 % test coverage**, race-clean, enforced in CI.
- **Reproducible** — the client is derived from source; the generator and the
  spec both live in the tree, and CI fails if the checked-in generated files
  drift from what the spec produces.

## Install

```sh
go get github.com/go-dimail/dimail
```

## Authentication

Dimail authenticates with HTTP Basic credentials to mint a bearer token, then
expects that token on every other call. A `Client` sends a bearer token when
one is set and otherwise falls back to the Basic credentials, so `Login`
naturally reaches the token endpoint with Basic auth:

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/go-dimail/dimail"
)

func main() {
	ctx := context.Background()

	c := dimail.NewClient(dimail.WithBasicAuth("apiuser", "apipass"))
	if _, err := c.Login(ctx); err != nil { // fetches and stores a bearer token
		log.Fatal(err)
	}

	// List the domains this user administers.
	domains, err := c.GetDomains(ctx)
	if err != nil {
		log.Fatal(err)
	}
	for _, d := range domains {
		fmt.Printf("%s (state=%s)\n", d.Name, d.State)
	}

	// Create a mailbox (v2 API).
	mb, err := c.PostMailboxV2(ctx, "example.gouv.fr", "jean.dupont", &dimail.CreateMailbox2{
		Features: []dimail.MailboxFeature{dimail.MailboxFeatureOX},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("created:", mb.Email)
}
```

If you already hold a token, skip `Login`:

```go
c := dimail.NewClient(dimail.WithToken(os.Getenv("DIMAIL_TOKEN")))
```

## Errors

Every non-2xx response is returned as an `*dimail.APIError`, which carries the
status code, the request that produced it, the raw body, and the FastAPI
`detail` field when present. Convenience predicates cover the common cases:

```go
_, err := c.GetDomain(ctx, "absent.example")
var apiErr *dimail.APIError
if errors.As(err, &apiErr) && apiErr.NotFound() {
	// handle 404
}
```

`Unauthorized()`, `Forbidden()` and `Conflict()` are also provided.

## Configuration

`NewClient` accepts functional options:

| Option | Purpose |
| --- | --- |
| `WithBaseURL(u)` | Override the API root (defaults to production). |
| `WithHTTPClient(h)` | Supply your own `*http.Client` (timeouts, proxy, TLS). |
| `WithBasicAuth(user, pass)` | Credentials used to obtain a token. |
| `WithToken(token)` | Authenticate every request with a bearer token. |
| `WithUserAgent(ua)` | Override the `User-Agent` header. |

## Layout

| Path | Contents |
| --- | --- |
| `client.go` | Hand-written runtime: `Client`, auth, transport, generic decoders, `APIError`. |
| `models_gen.go` | Generated typed models (96 types: structs, string enums, scalar aliases). |
| `client_gen.go` | Generated request methods (one per OpenAPI operation). |
| `client_gen_smoke_test.go` | Generated test exercising every method. |
| `openapi.json` | The upstream OpenAPI 3.1 document (the source of truth). |
| `internal/gen` | The OpenAPI → Go generator. |

## License

BSD-3-Clause. See [LICENSE](LICENSE).

This is an independent client library; it is not affiliated with or endorsed by
DINUM or the *La Suite numérique* team.
