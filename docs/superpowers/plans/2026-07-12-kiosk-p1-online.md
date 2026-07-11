# Kiosk (P1, online) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the Phase-1 patient kiosk: a responsive PWA (mobile → OTP → per-purpose consent → done) fronted by a small stateless Go BFF that keeps the hospital API key server-side.

**Architecture:** `frontend/kiosk/` (Vite+React+TS PWA) makes same-origin calls to `/kiosk/api/*`. A new `kiosk-bff/` (Gin, no DB/Redis/session) mints a hospital JWT from the API key and proxies three POSTs to notification-service and consent-service. The gateway routes `/kiosk/*` to the BFF, which also serves the built PWA. The hospital-JWT token client is promoted from admin-bff to `shared/hospitaljwt` so both BFFs share one copy.

**Tech Stack:** Go 1.25 + Gin (BFF), Vite 8 + React 19 + TypeScript (PWA), Vitest + Testing Library (PWA tests), Caddy (gateway), Docker Compose. Mirrors `admin-bff/` and `frontend/admin-dashboard/`.

## Global Constraints

- Go module path prefix: `github.com/hiabhi-cpu/<service>`. Every Go module uses `replace github.com/hiabhi-cpu/shared => ../shared`.
- Go BFFs run `SetTrustedProxies(nil)` and `gin.ReleaseMode` (see `admin-bff/cmd/server/main.go`).
- The hospital API key and hospital JWT are **server-side only** — they must never appear in any response sent to the browser.
- Downstream service endpoints (both require a hospital JWT via `Authorization: Bearer`):
  - `POST {NOTIFICATION_SERVICE_URL}/api/v1/otp/send` body `{"mobile":"<10 digits>"}` → `{"reference_id","expires_at"}`
  - `POST {NOTIFICATION_SERVICE_URL}/api/v1/otp/verify` body `{"reference_id","otp":"<6 digits>","mobile"}` → `{"session_id"}`
  - `POST {CONSENT_SERVICE_URL}/api/v1/consent/capture` body `{"mobile","session_id","purposes":["k1",...]}` → 201 (new) / 200 (replay)
- Kiosk-bff listens on **9008**. PWA is served under base path **`/kiosk/`**; its API calls go to **`/kiosk/api/*`**.
- Docker build context is the **repo root** (`context: ..`) because of the `replace ../shared`.
- New Go module must be added to `go.work`.

---

### Task 1: Promote hospital-JWT token client to `shared/hospitaljwt`

Move the API-key→JWT client out of admin-bff so kiosk-bff can reuse it without pulling admin-bff's DB/bcrypt deps.

**Files:**
- Create: `shared/hospitaljwt/token.go` (moved from `admin-bff/pkg/auth/token.go`, package renamed)
- Create: `shared/hospitaljwt/token_test.go` (moved from `admin-bff/pkg/auth/token_test.go`)
- Delete: `admin-bff/pkg/auth/token.go`, `admin-bff/pkg/auth/token_test.go`
- Modify: `admin-bff/pkg/handlers/proxy.go` (import + `auth.TokenProvider` → `hospitaljwt.TokenProvider`)
- Modify: `admin-bff/cmd/server/main.go` (import + `auth.NewHospitalTokenClient` → `hospitaljwt.NewHospitalTokenClient`)

**Interfaces:**
- Produces: package `hospitaljwt` with
  - `type TokenProvider interface { Token(ctx context.Context) (string, error) }`
  - `func NewHospitalTokenClient(authURL, apiKey string) *HospitalTokenClient`
  - `HospitalTokenClient` implements `TokenProvider` (caches until 60s before expiry; exchanges via `POST {authURL}/v1/auth/token` body `{"api_key":...}` → `{"token","expires_at"}`).

- [ ] **Step 1: Create the shared package by moving token.go**

Create `shared/hospitaljwt/token.go` with the exact body of `admin-bff/pkg/auth/token.go` but with the first line changed to:

```go
package hospitaljwt
```

(Everything else in that file is unchanged — it only imports `bytes`, `context`, `encoding/json`, `fmt`, `net/http`, `sync`, `time`.)

- [ ] **Step 2: Move the test**

Create `shared/hospitaljwt/token_test.go` with the body of `admin-bff/pkg/auth/token_test.go`, changing its package declaration to `package hospitaljwt` (if it was `package auth`) or `package hospitaljwt_test` (if it was `package auth_test`), and fixing any `auth.` references to the local package.

- [ ] **Step 3: Delete the originals**

```bash
git rm admin-bff/pkg/auth/token.go admin-bff/pkg/auth/token_test.go
```

- [ ] **Step 4: Update admin-bff proxy.go**

In `admin-bff/pkg/handlers/proxy.go`: remove the `"github.com/hiabhi-cpu/admin-bff/pkg/auth"` import if it is now only used for the token type, add `"github.com/hiabhi-cpu/shared/hospitaljwt"`, and change the `Proxy.token` field type and `NewProxy` parameter from `auth.TokenProvider` to `hospitaljwt.TokenProvider`. (Keep the `auth` import if other symbols from it are still used — check first.)

- [ ] **Step 5: Update admin-bff main.go**

In `admin-bff/cmd/server/main.go`: add `"github.com/hiabhi-cpu/shared/hospitaljwt"` and change `auth.NewHospitalTokenClient(...)` to `hospitaljwt.NewHospitalTokenClient(...)`. Leave the other `auth.` uses (users repo) untouched.

- [ ] **Step 6: Tidy and run admin-bff + shared tests**

Run:
```bash
cd shared && go mod tidy && go test ./hospitaljwt/... && cd ..
cd admin-bff && go mod tidy && go build ./... && go test ./... && cd ..
```
Expected: shared hospitaljwt tests PASS; admin-bff builds and all existing tests PASS.

- [ ] **Step 7: Commit**

```bash
git add shared/hospitaljwt admin-bff shared/go.mod shared/go.sum admin-bff/go.mod admin-bff/go.sum
git commit -m "refactor(shared): promote hospital-JWT token client to shared/hospitaljwt"
```

---

### Task 2: kiosk-bff module, env, and POST proxy

Scaffold the module and the one handler that matters: forward a POST to a downstream service with the hospital JWT attached, never leaking the token.

**Files:**
- Create: `kiosk-bff/go.mod`
- Create: `kiosk-bff/bootstrap/env.go`
- Create: `kiosk-bff/pkg/handlers/proxy.go`
- Test: `kiosk-bff/pkg/handlers/proxy_test.go`
- Modify: `go.work` (add `./kiosk-bff`)

**Interfaces:**
- Consumes: `hospitaljwt.TokenProvider` (Task 1).
- Produces:
  - `bootstrap.Env` with fields `Port, HospitalAPIKey, AuthServiceURL, NotificationServiceURL, ConsentServiceURL, StaticDir string` and `func NewEnv() *Env`.
  - `handlers.Proxy` with `func NewProxy(base string, token hospitaljwt.TokenProvider) *Proxy` and `func (p *Proxy) ForwardPost(c *gin.Context, downstreamPath string)`.

- [ ] **Step 1: Create go.mod and register in go.work**

Create `kiosk-bff/go.mod`:

```go
module github.com/hiabhi-cpu/kiosk-bff

go 1.25

require (
	github.com/gin-gonic/gin v1.10.0
	github.com/hiabhi-cpu/shared v0.0.0-00010101000000-000000000000
	github.com/joho/godotenv v1.5.1
	github.com/sirupsen/logrus v1.9.3
)

replace github.com/hiabhi-cpu/shared => ../shared
```

Add `./kiosk-bff` to the `use (...)` block in `go.work` (repo root), keeping the list alphabetical:

```
use (
	./admin-bff
	./audit-service
	./auth-service
	./consent-service
	./emergency-service
	./kiosk-bff
	./notification-service
	./shared
	./tools/repograph
)
```

- [ ] **Step 2: Write env.go**

Create `kiosk-bff/bootstrap/env.go`:

```go
package bootstrap

import (
	"fmt"
	"os"
)

// Env holds all configuration for kiosk-bff loaded from environment variables.
type Env struct {
	Port                   string
	HospitalAPIKey         string // raw hospital API key — server-side secret, never sent to the browser
	AuthServiceURL         string
	NotificationServiceURL string
	ConsentServiceURL      string
	StaticDir              string // path to the built PWA; empty disables static serving
}

// NewEnv loads and validates required environment variables.
func NewEnv() *Env {
	return &Env{
		Port:                   mustGet("KIOSK_BFF_PORT"),
		HospitalAPIKey:         mustGet("HOSPITAL_API_KEY"),
		AuthServiceURL:         mustGet("AUTH_SERVICE_URL"),
		NotificationServiceURL: mustGet("NOTIFICATION_SERVICE_URL"),
		ConsentServiceURL:      mustGet("CONSENT_SERVICE_URL"),
		StaticDir:              os.Getenv("STATIC_DIR"),
	}
}

func mustGet(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic(fmt.Sprintf("bootstrap: required env var %q is not set", key))
	}
	return v
}
```

- [ ] **Step 3: Write the failing proxy test**

Create `kiosk-bff/pkg/handlers/proxy_test.go`:

```go
package handlers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

type stubToken struct{ tok string; err error }

func (s stubToken) Token(context.Context) (string, error) { return s.tok, s.err }

func TestForwardPost_AttachesBearerAndPipesBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var gotAuth, gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"session_id":"sess-1"}`))
	}))
	defer upstream.Close()

	p := NewProxy(upstream.URL, stubToken{tok: "secret-jwt"})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/kiosk/api/x", strings.NewReader(`{"mobile":"9999999999"}`))

	p.ForwardPost(c, "/api/v1/otp/verify")

	if gotAuth != "Bearer secret-jwt" {
		t.Fatalf("upstream Authorization = %q, want Bearer secret-jwt", gotAuth)
	}
	if gotBody != `{"mobile":"9999999999"}` {
		t.Fatalf("upstream body = %q", gotBody)
	}
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", w.Code)
	}
	if strings.Contains(w.Body.String(), "secret-jwt") {
		t.Fatalf("JWT leaked into browser response: %s", w.Body.String())
	}
}

func TestForwardPost_TokenFailureIs502(t *testing.T) {
	gin.SetMode(gin.TestMode)
	p := NewProxy("http://unused", stubToken{err: io.EOF})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/kiosk/api/x", strings.NewReader(`{}`))

	p.ForwardPost(c, "/api/v1/otp/send")

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", w.Code)
	}
}
```

- [ ] **Step 4: Run the test to verify it fails**

Run: `cd kiosk-bff && go test ./pkg/handlers/... 2>&1 | head`
Expected: FAIL — `NewProxy`/`ForwardPost` undefined (package won't compile).

- [ ] **Step 5: Write proxy.go**

Create `kiosk-bff/pkg/handlers/proxy.go`:

```go
package handlers

import (
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	"github.com/hiabhi-cpu/shared/hospitaljwt"
)

// Proxy forwards a request to one downstream service, attaching the hospital JWT.
// The JWT and API key stay server-side — only the downstream body is piped back.
type Proxy struct {
	base   string
	token  hospitaljwt.TokenProvider
	client *http.Client
}

// NewProxy builds a Proxy for the given downstream base URL.
func NewProxy(base string, token hospitaljwt.TokenProvider) *Proxy {
	return &Proxy{base: base, token: token, client: &http.Client{Timeout: 10 * time.Second}}
}

// ForwardPost proxies the incoming POST body to base+downstreamPath with the
// Bearer hospital JWT attached, then pipes the downstream status and body back.
func (p *Proxy) ForwardPost(c *gin.Context, downstreamPath string) {
	tok, err := p.token.Token(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "auth upstream unavailable"})
		return
	}

	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, p.base+downstreamPath, c.Request.Body)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "bad upstream request"})
		return
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "upstream unavailable"})
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("kiosk proxy: read downstream body: %v", err)
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/json"
	}
	c.Data(resp.StatusCode, ct, body)
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `cd kiosk-bff && go mod tidy && go test ./...`
Expected: PASS (both proxy tests).

- [ ] **Step 7: Commit**

```bash
git add go.work kiosk-bff
git commit -m "feat(kiosk-bff): module scaffold, env, and JWT-attaching POST proxy"
```

---

### Task 3: kiosk-bff routes, static serving, main wiring, gateway route, Docker

Wire the three routes + PWA static serving, add the container, and route `/kiosk/*` at the gateway.

**Files:**
- Create: `kiosk-bff/pkg/routes/routes.go`
- Create: `kiosk-bff/cmd/server/main.go`
- Create: `kiosk-bff/Dockerfile`
- Create: `kiosk-bff/docker-compose.yml`
- Test: `kiosk-bff/pkg/routes/routes_test.go`
- Modify: `gateway/Caddyfile` (add `/kiosk/*` route before the admin-bff catch-all)
- Modify: `gateway/test-routes.sh` (add a kiosk route assertion)

**Interfaces:**
- Consumes: `handlers.Proxy` (Task 2), `bootstrap.Env` (Task 2), `hospitaljwt.NewHospitalTokenClient` (Task 1).
- Produces: `func routes.Setup(r *gin.Engine, d routes.Deps)` where `Deps{ OTP, Consent *handlers.Proxy; StaticDir string }`. Registers:
  - `POST /kiosk/api/otp/send` → OTP `/api/v1/otp/send`
  - `POST /kiosk/api/otp/verify` → OTP `/api/v1/otp/verify`
  - `POST /kiosk/api/consent/capture` → Consent `/api/v1/consent/capture`
  - `GET /health`; static PWA under `/kiosk/` with SPA fallback when `StaticDir != ""`.

- [ ] **Step 1: Write the failing routes test**

Create `kiosk-bff/pkg/routes/routes_test.go`:

```go
package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hiabhi-cpu/kiosk-bff/pkg/handlers"
)

func newTestRouter(upstreamURL string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// Both proxies point at the same fake upstream for routing assertions.
	tp := handlers.StubProvider("test-jwt")
	Setup(r, Deps{
		OTP:       handlers.NewProxy(upstreamURL, tp),
		Consent:   handlers.NewProxy(upstreamURL, tp),
		StaticDir: "",
	})
	return r
}

func TestRoutesReachUpstream(t *testing.T) {
	var hits []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	r := newTestRouter(upstream.URL)

	cases := map[string]string{
		"/kiosk/api/otp/send":        "/api/v1/otp/send",
		"/kiosk/api/otp/verify":      "/api/v1/otp/verify",
		"/kiosk/api/consent/capture": "/api/v1/consent/capture",
	}
	for path, wantUpstream := range cases {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, http.NoBody)
		r.ServeHTTP(w, req)
		if w.Code == http.StatusNotFound {
			t.Fatalf("%s not routed (404)", path)
		}
		found := false
		for _, h := range hits {
			if h == wantUpstream {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s did not reach upstream %s; hits=%v", path, wantUpstream, hits)
		}
	}
}
```

Add a tiny test helper to the handlers package so tests can build a provider without redeclaring the interface. Append to `kiosk-bff/pkg/handlers/proxy.go`:

```go
// StubProvider returns a TokenProvider that always yields tok (test helper).
func StubProvider(tok string) hospitaljwt.TokenProvider { return stubProvider(tok) }

type stubProvider string

func (s stubProvider) Token(_ context.Context) (string, error) { return string(s), nil }
```

and add `"context"` to that file's imports (it is needed for `stubProvider.Token`'s signature).

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd kiosk-bff && go test ./pkg/routes/... 2>&1 | head`
Expected: FAIL — `Setup`, `Deps` undefined.

- [ ] **Step 3: Write routes.go**

Create `kiosk-bff/pkg/routes/routes.go`:

```go
package routes

import (
	"net/http"
	"path/filepath"

	"github.com/gin-gonic/gin"

	"github.com/hiabhi-cpu/kiosk-bff/pkg/handlers"
)

// Deps bundles what the routes need.
type Deps struct {
	OTP       *handlers.Proxy
	Consent   *handlers.Proxy
	StaticDir string // built PWA; empty disables static serving (dev uses the Vite server)
}

// Setup registers all kiosk-bff routes.
func Setup(r *gin.Engine, d Deps) {
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "service": "kiosk-bff"})
	})

	api := r.Group("/kiosk/api")
	{
		api.POST("/otp/send", func(c *gin.Context) { d.OTP.ForwardPost(c, "/api/v1/otp/send") })
		api.POST("/otp/verify", func(c *gin.Context) { d.OTP.ForwardPost(c, "/api/v1/otp/verify") })
		api.POST("/consent/capture", func(c *gin.Context) { d.Consent.ForwardPost(c, "/api/v1/consent/capture") })
	}

	if d.StaticDir != "" {
		// Serve built assets and index.html under /kiosk/. SPA fallback: any
		// unmatched /kiosk/* GET returns index.html so client routing works.
		r.Static("/kiosk/assets", filepath.Join(d.StaticDir, "assets"))
		index := filepath.Join(d.StaticDir, "index.html")
		serveIndex := func(c *gin.Context) { c.File(index) }
		r.GET("/kiosk", serveIndex)
		r.NoRoute(func(c *gin.Context) {
			if c.Request.Method == http.MethodGet &&
				len(c.Request.URL.Path) >= 6 && c.Request.URL.Path[:6] == "/kiosk" {
				serveIndex(c)
				return
			}
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		})
	}
}
```

- [ ] **Step 4: Run the routes test to verify it passes**

Run: `cd kiosk-bff && go test ./...`
Expected: PASS.

- [ ] **Step 5: Write main.go**

Create `kiosk-bff/cmd/server/main.go`:

```go
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	_ "github.com/joho/godotenv/autoload"
	log "github.com/sirupsen/logrus"

	"github.com/hiabhi-cpu/kiosk-bff/bootstrap"
	"github.com/hiabhi-cpu/kiosk-bff/pkg/handlers"
	"github.com/hiabhi-cpu/kiosk-bff/pkg/routes"
	"github.com/hiabhi-cpu/shared/hospitaljwt"
	"github.com/hiabhi-cpu/shared/logging"
)

func main() {
	logging.Setup("kiosk-bff")

	env := bootstrap.NewEnv()
	tokens := hospitaljwt.NewHospitalTokenClient(env.AuthServiceURL, env.HospitalAPIKey)

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	_ = r.SetTrustedProxies(nil)
	r.Use(gin.Recovery(), gin.Logger())
	routes.Setup(r, routes.Deps{
		OTP:       handlers.NewProxy(env.NotificationServiceURL, tokens),
		Consent:   handlers.NewProxy(env.ConsentServiceURL, tokens),
		StaticDir: env.StaticDir,
	})

	addr := fmt.Sprintf(":%s", env.Port)
	srv := &http.Server{Addr: addr, Handler: r, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		log.Printf("kiosk-bff listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("kiosk-bff: server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	log.Println("kiosk-bff: stopped")
}
```

- [ ] **Step 6: Verify the whole module builds**

Run: `cd kiosk-bff && go mod tidy && go build ./... && go test ./...`
Expected: builds clean, all tests PASS.

- [ ] **Step 7: Write the Dockerfile**

Create `kiosk-bff/Dockerfile` (mirror of `admin-bff/Dockerfile`, no DB tools needed):

```dockerfile
# syntax=docker/dockerfile:1
# Build context MUST be the repo root (parent of this dir) because go.mod uses
#   replace github.com/hiabhi-cpu/shared => ../shared
# Via compose this is handled by `context: ..`. Manual build from the repo root:
#   docker build -f kiosk-bff/Dockerfile -t kiosk-bff .

FROM golang:1.25-alpine AS builder
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY shared/go.mod shared/go.sum ./shared/
COPY kiosk-bff/go.mod kiosk-bff/go.sum ./kiosk-bff/
WORKDIR /app/kiosk-bff
RUN go mod download
WORKDIR /app
COPY shared/ ./shared/
COPY kiosk-bff/ ./kiosk-bff/
WORKDIR /app/kiosk-bff
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o /out/kiosk-bff ./cmd/server

FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /out/kiosk-bff .
RUN adduser -D appuser
USER appuser
EXPOSE 9008
CMD ["./kiosk-bff"]
```

- [ ] **Step 8: Write docker-compose.yml**

Create `kiosk-bff/docker-compose.yml` (mirror admin-bff; no DB/Redis). `STATIC_DIR` is left unset here — the PWA build is mounted/served in Task 5's integration step; the container runs API-only until then.

```yaml
# kiosk-bff — standalone compose (polyrepo style).
# Requires the shared external dpdp-network with auth/notification/consent
# services reachable on it — see ../DOCKER.md.
# Run from this directory:  docker compose up -d --build
# Build context is the repo root (..) because go.mod uses `replace ../shared`.
services:
  kiosk-bff:
    container_name: dpdp-kiosk-bff
    build:
      context: ..
      dockerfile: kiosk-bff/Dockerfile
    environment:
      KIOSK_BFF_PORT: "9008"
      HOSPITAL_API_KEY: ${HOSPITAL_API_KEY:-TEST-HOSPITAL-API-KEY-LOCAL-DEV-001}
      AUTH_SERVICE_URL: "http://auth-service:9006"
      NOTIFICATION_SERVICE_URL: "http://notification-service:9004"
      CONSENT_SERVICE_URL: "http://consent-service:9000"
    ports:
      - "9008:9008"
    volumes:
      - /data/logs:/data/logs
    restart: unless-stopped
    networks:
      - dpdp-network
    healthcheck:
      test: ["CMD", "wget", "-q", "-O", "-", "http://localhost:9008/health"]
      interval: 10s
      timeout: 3s
      retries: 5
      start_period: 10s

networks:
  dpdp-network:
    external: true
```

- [ ] **Step 9: Add the gateway route**

In `gateway/Caddyfile`, add the kiosk route **immediately before** the catch-all `reverse_proxy admin-bff:9007` line (so `/kiosk/*` is matched before the admin catch-all):

```
			@kiosk path /kiosk/*
			reverse_proxy @kiosk kiosk-bff:9008

			# Catch-all: dashboard SPA + BFF cookie /api/* session API.
			reverse_proxy admin-bff:9007
```

- [ ] **Step 10: Add a gateway route assertion**

In `gateway/test-routes.sh`, add a check that `/kiosk/api/otp/send` routes to kiosk-bff (follow the existing assertion style in that file — match on the upstream reached or a health probe). Add a line asserting a POST to `/kiosk/api/otp/send` does **not** 404 at the gateway.

- [ ] **Step 11: Commit**

```bash
git add kiosk-bff gateway/Caddyfile gateway/test-routes.sh
git commit -m "feat(kiosk-bff): routes, static serving, main, Docker, gateway /kiosk route"
```

---

### Task 4: PWA scaffold — Vite + React + TS, manifest, old-browser fallback, notice data

Stand up `frontend/kiosk/` mirroring `frontend/admin-dashboard/`, with the base path, PWA manifest, `nomodule` fallback, and the bundled static notice.

**Files:**
- Create: `frontend/kiosk/package.json`, `frontend/kiosk/vite.config.ts`, `frontend/kiosk/index.html`
- Create: `frontend/kiosk/tsconfig.json`, `frontend/kiosk/tsconfig.app.json`, `frontend/kiosk/tsconfig.node.json`
- Create: `frontend/kiosk/vitest.setup.ts`, `frontend/kiosk/.gitignore`
- Create: `frontend/kiosk/public/manifest.webmanifest`
- Create: `frontend/kiosk/src/data/notice.ts`
- Create: `frontend/kiosk/src/main.tsx`, `frontend/kiosk/src/App.tsx` (placeholder)

**Interfaces:**
- Produces: `src/data/notice.ts` exporting
  - `export interface Purpose { key: string; label: string; description: string }`
  - `export const NOTICE_TEXT: string`
  - `export const PURPOSES: Purpose[]`

- [ ] **Step 1: Copy tooling config from admin-dashboard**

Copy these files verbatim from `frontend/admin-dashboard/` to `frontend/kiosk/`, then edit as noted: `tsconfig.json`, `tsconfig.app.json`, `tsconfig.node.json`, `vitest.setup.ts`, `.gitignore`, `.oxlintrc.json`.

- [ ] **Step 2: Write package.json**

Create `frontend/kiosk/package.json` (same stack as admin-dashboard, minus recharts/react-router which the kiosk doesn't need):

```json
{
  "name": "kiosk",
  "private": true,
  "version": "0.0.0",
  "type": "module",
  "scripts": {
    "dev": "vite",
    "build": "tsc -b && vite build",
    "lint": "oxlint",
    "preview": "vite preview",
    "test": "vitest run",
    "test:watch": "vitest"
  },
  "dependencies": {
    "react": "^19.2.7",
    "react-dom": "^19.2.7"
  },
  "devDependencies": {
    "@testing-library/jest-dom": "^6.9.1",
    "@testing-library/react": "^16.3.2",
    "@testing-library/user-event": "^14.6.1",
    "@types/node": "^24.13.2",
    "@types/react": "^19.2.17",
    "@types/react-dom": "^19.2.3",
    "@vitejs/plugin-react": "^6.0.3",
    "jsdom": "^29.1.1",
    "oxlint": "^1.71.0",
    "typescript": "~6.0.2",
    "vite": "^8.1.1",
    "vitest": "^4.1.10"
  }
}
```

- [ ] **Step 3: Write vite.config.ts with base path and dev proxy**

Create `frontend/kiosk/vite.config.ts`:

```ts
import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

export default defineConfig({
  base: "/kiosk/",
  plugins: [react()],
  build: {
    // ponytail: browser floor for ~5-year-old devices; drop anything older.
    target: ["es2021", "chrome90", "safari14"],
  },
  server: {
    port: 5174,
    proxy: {
      // Same-origin from the browser; Vite forwards /kiosk/api to the BFF.
      "/kiosk/api": { target: "http://localhost:9008", changeOrigin: true },
    },
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./vitest.setup.ts"],
  },
});
```

- [ ] **Step 4: Write index.html with the nomodule fallback**

Create `frontend/kiosk/index.html`. The `<script nomodule>` block shows an update-your-browser message on browsers too old to run ES-module bundles — no detection code, the platform does it.

```html
<!doctype html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <link rel="icon" type="image/svg+xml" href="/kiosk/favicon.svg" />
    <link rel="manifest" href="/kiosk/manifest.webmanifest" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0, viewport-fit=cover" />
    <meta name="theme-color" content="#0b5" />
    <title>Consent</title>
  </head>
  <body>
    <div id="root"></div>
    <noscript>Please enable JavaScript to give consent.</noscript>
    <script nomodule>
      document.body.innerHTML =
        '<div style="font-family:sans-serif;padding:2rem;text-align:center">' +
        'Your browser is out of date. Please update it or use a newer device to continue.</div>';
    </script>
    <script type="module" src="/src/main.tsx"></script>
  </body>
</html>
```

- [ ] **Step 5: Write the PWA manifest**

Create `frontend/kiosk/public/manifest.webmanifest`:

```json
{
  "name": "DPDP Consent",
  "short_name": "Consent",
  "start_url": "/kiosk/",
  "scope": "/kiosk/",
  "display": "fullscreen",
  "orientation": "any",
  "background_color": "#ffffff",
  "theme_color": "#00bb55"
}
```

Also copy an icon: `cp frontend/admin-dashboard/public/favicon.svg frontend/kiosk/public/favicon.svg`.

- [ ] **Step 6: Write the bundled notice data**

Create `frontend/kiosk/src/data/notice.ts`:

```ts
export interface Purpose {
  key: string;
  label: string;
  description: string;
}

// ponytail: static in P1. A dynamic /kiosk/api/notice endpoint arrives in P2
// with multi-language/managed content (see the spec's out-of-scope list).
export const NOTICE_TEXT =
  "This hospital will process your personal and health data to provide care. " +
  "Under the Digital Personal Data Protection Act, your consent is required for " +
  "each purpose below. You may decline any purpose, and you can withdraw consent later.";

export const PURPOSES: Purpose[] = [
  { key: "treatment", label: "Treatment & care", description: "Use your data to diagnose and treat you." },
  { key: "records", label: "Medical records", description: "Maintain your medical history for continuity of care." },
  { key: "billing", label: "Billing & insurance", description: "Process payments and insurance claims." },
];
```

- [ ] **Step 7: Write main.tsx and a placeholder App**

Create `frontend/kiosk/src/main.tsx`:

```tsx
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import "./styles/global.css";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
```

Create `frontend/kiosk/src/App.tsx` (placeholder, replaced in Task 5):

```tsx
export function App() {
  return <main>kiosk</main>;
}
```

Create an empty `frontend/kiosk/src/styles/global.css` (filled in Task 5) so the import resolves.

- [ ] **Step 8: Install, build, and verify**

Run:
```bash
cd frontend/kiosk && npm install && npm run build
```
Expected: `npm run build` succeeds; `dist/index.html` exists and contains the `nomodule` script. Verify:
```bash
grep -q nomodule dist/index.html && echo "fallback present"
```
Expected: prints `fallback present`.

- [ ] **Step 9: Commit**

```bash
git add frontend/kiosk
git commit -m "feat(kiosk): PWA scaffold — Vite base path, manifest, nomodule fallback, notice data"
```

---

### Task 5: PWA consent wizard — API client, state machine, steps, fluid styles

Build the actual flow and its tests: welcome → mobile → OTP → per-purpose consent → done, with reset-on-done and inline error retry.

**Files:**
- Create: `frontend/kiosk/src/api/kiosk.ts`
- Test: `frontend/kiosk/src/api/kiosk.test.ts`
- Modify: `frontend/kiosk/src/App.tsx` (the wizard)
- Create: `frontend/kiosk/src/steps/Welcome.tsx`, `Mobile.tsx`, `Otp.tsx`, `Consent.tsx`, `Done.tsx`
- Modify: `frontend/kiosk/src/styles/global.css` (fluid layout, ≥44px targets)
- Test: `frontend/kiosk/src/App.test.tsx`

**Interfaces:**
- Consumes: `NOTICE_TEXT`, `PURPOSES`, `Purpose` (Task 4).
- Produces:
  - `src/api/kiosk.ts`: `sendOtp(mobile: string): Promise<{ reference_id: string }>`, `verifyOtp(mobile: string, referenceId: string, otp: string): Promise<{ session_id: string }>`, `capture(mobile: string, sessionId: string, purposes: string[]): Promise<void>`, and `class ApiError extends Error { status: number }`.

- [ ] **Step 1: Write the failing API-client test**

Create `frontend/kiosk/src/api/kiosk.test.ts`:

```ts
import { describe, it, expect, vi, afterEach } from "vitest";
import { sendOtp, verifyOtp, capture, ApiError } from "./kiosk";

afterEach(() => vi.restoreAllMocks());

describe("kiosk api", () => {
  it("posts mobile to /kiosk/api/otp/send and returns reference_id", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ reference_id: "ref-1" }), { status: 200 }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const out = await sendOtp("9999999999");

    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/kiosk/api/otp/send");
    expect(JSON.parse(init.body)).toEqual({ mobile: "9999999999" });
    expect(out.reference_id).toBe("ref-1");
  });

  it("throws ApiError on non-2xx", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ error: "invalid or expired OTP" }), { status: 401 }),
    ));
    await expect(verifyOtp("9999999999", "ref-1", "000000")).rejects.toBeInstanceOf(ApiError);
  });

  it("capture posts session_id + purposes", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response("{}", { status: 201 }));
    vi.stubGlobal("fetch", fetchMock);

    await capture("9999999999", "sess-1", ["treatment"]);

    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/kiosk/api/consent/capture");
    expect(JSON.parse(init.body)).toEqual({
      mobile: "9999999999",
      session_id: "sess-1",
      purposes: ["treatment"],
    });
  });
});
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd frontend/kiosk && npx vitest run src/api/kiosk.test.ts`
Expected: FAIL — module `./kiosk` not found.

- [ ] **Step 3: Write the API client**

Create `frontend/kiosk/src/api/kiosk.ts`:

```ts
export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
    this.name = "ApiError";
  }
}

async function post<T>(path: string, body: unknown): Promise<T> {
  const resp = await fetch(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!resp.ok) {
    let msg = `request failed (${resp.status})`;
    try {
      const j = await resp.json();
      if (j?.error) msg = j.error;
    } catch {
      // non-JSON error body; keep the default message
    }
    throw new ApiError(resp.status, msg);
  }
  const text = await resp.text();
  return (text ? JSON.parse(text) : {}) as T;
}

export function sendOtp(mobile: string): Promise<{ reference_id: string }> {
  return post("/kiosk/api/otp/send", { mobile });
}

export function verifyOtp(
  mobile: string,
  referenceId: string,
  otp: string,
): Promise<{ session_id: string }> {
  return post("/kiosk/api/otp/verify", { mobile, reference_id: referenceId, otp });
}

export function capture(mobile: string, sessionId: string, purposes: string[]): Promise<void> {
  return post("/kiosk/api/consent/capture", { mobile, session_id: sessionId, purposes });
}
```

- [ ] **Step 4: Run the API test to verify it passes**

Run: `cd frontend/kiosk && npx vitest run src/api/kiosk.test.ts`
Expected: PASS (3 tests).

- [ ] **Step 5: Write the failing wizard test**

Create `frontend/kiosk/src/App.test.tsx`:

```tsx
import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { App } from "./App";

afterEach(() => vi.restoreAllMocks());

function mockFetchSequence(responses: Response[]) {
  const fn = vi.fn();
  responses.forEach((r) => fn.mockResolvedValueOnce(r));
  vi.stubGlobal("fetch", fn);
  return fn;
}

describe("consent wizard", () => {
  it("walks mobile → otp → consent → done and resets", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const user = userEvent.setup();
    mockFetchSequence([
      new Response(JSON.stringify({ reference_id: "ref-1" }), { status: 200 }),
      new Response(JSON.stringify({ session_id: "sess-1" }), { status: 200 }),
      new Response("{}", { status: 201 }),
    ]);

    render(<App />);
    await user.click(screen.getByRole("button", { name: /start/i }));

    await user.type(screen.getByLabelText(/mobile/i), "9999999999");
    await user.click(screen.getByRole("button", { name: /send otp/i }));

    await user.type(await screen.findByLabelText(/otp/i), "123456");
    await user.click(screen.getByRole("button", { name: /verify/i }));

    // consent step: at least one purpose is granted by default; confirm.
    await user.click(await screen.findByRole("button", { name: /confirm/i }));

    expect(await screen.findByText(/thank you/i)).toBeInTheDocument();

    // auto-reset returns to the welcome screen.
    vi.advanceTimersByTime(6000);
    await waitFor(() => expect(screen.getByRole("button", { name: /start/i })).toBeInTheDocument());
    vi.useRealTimers();
  });

  it("shows an inline error and lets the patient retry when OTP is wrong", async () => {
    const user = userEvent.setup();
    mockFetchSequence([
      new Response(JSON.stringify({ reference_id: "ref-1" }), { status: 200 }),
      new Response(JSON.stringify({ error: "invalid or expired OTP" }), { status: 401 }),
    ]);

    render(<App />);
    await user.click(screen.getByRole("button", { name: /start/i }));
    await user.type(screen.getByLabelText(/mobile/i), "9999999999");
    await user.click(screen.getByRole("button", { name: /send otp/i }));
    await user.type(await screen.findByLabelText(/otp/i), "000000");
    await user.click(screen.getByRole("button", { name: /verify/i }));

    expect(await screen.findByText(/invalid or expired otp/i)).toBeInTheDocument();
    // still on the OTP step — can retry.
    expect(screen.getByLabelText(/otp/i)).toBeInTheDocument();
  });
});
```

- [ ] **Step 6: Run it to verify it fails**

Run: `cd frontend/kiosk && npx vitest run src/App.test.tsx`
Expected: FAIL — steps/UI not implemented (no Start button etc.).

- [ ] **Step 7: Write the step components**

Create `frontend/kiosk/src/steps/Welcome.tsx`:

```tsx
export function Welcome({ onStart }: { onStart: () => void }) {
  return (
    <section className="card">
      <h1>Consent</h1>
      <p>Please give your consent for how this hospital uses your data.</p>
      <button className="primary" onClick={onStart}>Start</button>
    </section>
  );
}
```

Create `frontend/kiosk/src/steps/Mobile.tsx`:

```tsx
import { useState } from "react";

export function Mobile({ busy, error, onSubmit }: {
  busy: boolean;
  error: string;
  onSubmit: (mobile: string) => void;
}) {
  const [mobile, setMobile] = useState("");
  return (
    <section className="card">
      <h2>Enter your mobile number</h2>
      <label htmlFor="mobile">Mobile number</label>
      <input
        id="mobile"
        inputMode="numeric"
        autoComplete="tel"
        maxLength={10}
        value={mobile}
        onChange={(e) => setMobile(e.target.value.replace(/\D/g, ""))}
      />
      {error && <p className="error" role="alert">{error}</p>}
      <button className="primary" disabled={busy || mobile.length !== 10} onClick={() => onSubmit(mobile)}>
        Send OTP
      </button>
    </section>
  );
}
```

Create `frontend/kiosk/src/steps/Otp.tsx`:

```tsx
import { useState } from "react";

export function Otp({ busy, error, onSubmit }: {
  busy: boolean;
  error: string;
  onSubmit: (otp: string) => void;
}) {
  const [otp, setOtp] = useState("");
  return (
    <section className="card">
      <h2>Enter the OTP</h2>
      <label htmlFor="otp">OTP</label>
      <input
        id="otp"
        inputMode="numeric"
        autoComplete="one-time-code"
        maxLength={6}
        value={otp}
        onChange={(e) => setOtp(e.target.value.replace(/\D/g, ""))}
      />
      {error && <p className="error" role="alert">{error}</p>}
      <button className="primary" disabled={busy || otp.length !== 6} onClick={() => onSubmit(otp)}>
        Verify
      </button>
    </section>
  );
}
```

Create `frontend/kiosk/src/steps/Consent.tsx`:

```tsx
import { useState } from "react";
import { NOTICE_TEXT, PURPOSES } from "../data/notice";

export function Consent({ busy, error, onConfirm }: {
  busy: boolean;
  error: string;
  onConfirm: (purposes: string[]) => void;
}) {
  // Purposes start granted; the patient unchecks to decline.
  const [granted, setGranted] = useState<Record<string, boolean>>(
    Object.fromEntries(PURPOSES.map((p) => [p.key, true])),
  );
  const toggle = (key: string) => setGranted((g) => ({ ...g, [key]: !g[key] }));
  const chosen = PURPOSES.filter((p) => granted[p.key]).map((p) => p.key);

  return (
    <section className="card">
      <h2>Consent notice</h2>
      <p className="notice">{NOTICE_TEXT}</p>
      <ul className="purposes">
        {PURPOSES.map((p) => (
          <li key={p.key}>
            <label>
              <input type="checkbox" checked={granted[p.key]} onChange={() => toggle(p.key)} />
              <span><strong>{p.label}</strong> — {p.description}</span>
            </label>
          </li>
        ))}
      </ul>
      {error && <p className="error" role="alert">{error}</p>}
      <button className="primary" disabled={busy || chosen.length === 0} onClick={() => onConfirm(chosen)}>
        Confirm
      </button>
    </section>
  );
}
```

Create `frontend/kiosk/src/steps/Done.tsx`:

```tsx
export function Done() {
  return (
    <section className="card">
      <h2>Thank you</h2>
      <p>Your consent has been recorded.</p>
    </section>
  );
}
```

- [ ] **Step 8: Write the wizard state machine (App.tsx)**

Replace `frontend/kiosk/src/App.tsx`:

```tsx
import { useEffect, useState } from "react";
import { sendOtp, verifyOtp, capture, ApiError } from "./api/kiosk";
import { Welcome } from "./steps/Welcome";
import { Mobile } from "./steps/Mobile";
import { Otp } from "./steps/Otp";
import { Consent } from "./steps/Consent";
import { Done } from "./steps/Done";

type Step = "welcome" | "mobile" | "otp" | "consent" | "done";

const RESET_MS = 5000;

export function App() {
  const [step, setStep] = useState<Step>("welcome");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [mobile, setMobile] = useState("");
  const [referenceId, setReferenceId] = useState("");
  const [sessionId, setSessionId] = useState("");

  function reset() {
    setStep("welcome");
    setBusy(false);
    setError("");
    setMobile("");
    setReferenceId("");
    setSessionId("");
  }

  // Kiosk hygiene: clear the screen a few seconds after completion so no
  // patient data lingers for the next person.
  useEffect(() => {
    if (step !== "done") return;
    const t = setTimeout(reset, RESET_MS);
    return () => clearTimeout(t);
  }, [step]);

  function msg(e: unknown): string {
    return e instanceof ApiError ? e.message : "Something went wrong. Please try again.";
  }

  async function onMobile(m: string) {
    setBusy(true);
    setError("");
    try {
      const { reference_id } = await sendOtp(m);
      setMobile(m);
      setReferenceId(reference_id);
      setStep("otp");
    } catch (e) {
      setError(msg(e));
    } finally {
      setBusy(false);
    }
  }

  async function onOtp(otp: string) {
    setBusy(true);
    setError("");
    try {
      const { session_id } = await verifyOtp(mobile, referenceId, otp);
      setSessionId(session_id);
      setStep("consent");
    } catch (e) {
      setError(msg(e));
    } finally {
      setBusy(false);
    }
  }

  async function onConfirm(purposes: string[]) {
    setBusy(true);
    setError("");
    try {
      await capture(mobile, sessionId, purposes);
      setStep("done");
    } catch (e) {
      setError(msg(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <main className="shell">
      {step === "welcome" && <Welcome onStart={() => setStep("mobile")} />}
      {step === "mobile" && <Mobile busy={busy} error={error} onSubmit={onMobile} />}
      {step === "otp" && <Otp busy={busy} error={error} onSubmit={onOtp} />}
      {step === "consent" && <Consent busy={busy} error={error} onConfirm={onConfirm} />}
      {step === "done" && <Done />}
    </main>
  );
}
```

- [ ] **Step 9: Write the fluid stylesheet**

Replace `frontend/kiosk/src/styles/global.css`. No fixed pixel widths on layout; `clamp()` scales type/spacing; touch targets ≥44px:

```css
:root {
  --gap: clamp(0.75rem, 2vw, 1.5rem);
  --fs: clamp(1rem, 1rem + 0.5vw, 1.25rem);
  font-family: system-ui, -apple-system, sans-serif;
}

* { box-sizing: border-box; }
body { margin: 0; }

.shell {
  min-height: 100vh;
  min-height: 100dvh;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: var(--gap);
}

.card {
  width: 100%;
  max-width: 34rem; /* relative cap, still fluid below it */
  display: flex;
  flex-direction: column;
  gap: var(--gap);
  font-size: var(--fs);
}

.card h1, .card h2 { margin: 0; }

input[type="text"], input[type="tel"], input:not([type="checkbox"]) {
  width: 100%;
  min-height: 44px;
  font-size: var(--fs);
  padding: 0.5rem 0.75rem;
}

button.primary {
  min-height: 48px;
  font-size: var(--fs);
  padding: 0.75rem 1rem;
  cursor: pointer;
}

.purposes { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: var(--gap); }
.purposes label { display: flex; gap: 0.75rem; align-items: flex-start; min-height: 44px; }
.purposes input[type="checkbox"] { width: 24px; height: 24px; margin-top: 0.15rem; }

.error { color: #b00020; margin: 0; }
.notice { line-height: 1.5; }
```

- [ ] **Step 10: Run the full PWA test suite**

Run: `cd frontend/kiosk && npx vitest run`
Expected: PASS — api tests + both wizard tests (happy path with reset, OTP-error retry).

- [ ] **Step 11: Guard against fixed-width layout regressions**

Add this test to the end of `frontend/kiosk/src/App.test.tsx` (imports already present):

```tsx
it("layout uses no fixed pixel widths on the shell/card", async () => {
  const css = await import("fs").then((fs) =>
    fs.readFileSync(new URL("./styles/global.css", import.meta.url), "utf8"),
  );
  // width declarations must be fluid (%/rem/vw/auto), never a hard px width.
  const widthPx = /(^|[^-])width:\s*\d+px/m.test(css);
  expect(widthPx).toBe(false);
});
```

Run: `cd frontend/kiosk && npx vitest run src/App.test.tsx`
Expected: PASS.

- [ ] **Step 12: Build the PWA**

Run: `cd frontend/kiosk && npm run build`
Expected: build succeeds; `dist/` produced with base `/kiosk/`.

- [ ] **Step 13: End-to-end smoke against the running stack**

Bring up the backend + BFF + gateway (per `DOCKER.md`), point `STATIC_DIR` at the built PWA (mount `frontend/kiosk/dist` into the kiosk-bff container and set `STATIC_DIR=/app/web`), then:
```bash
# from repo root, with the stack up
curl -s -X POST http://localhost:8080/kiosk/api/otp/send \
  -H 'Content-Type: application/json' -d '{"mobile":"9999999999"}' | head
```
Expected: a JSON `reference_id` (not a 404/502), proving the gateway → kiosk-bff → notification path works with the server-side JWT. Then open `http://localhost:8080/kiosk/` in a browser (resize the window narrow→wide) and walk the flow to a "Thank you" screen.

- [ ] **Step 14: Commit**

```bash
git add frontend/kiosk
git commit -m "feat(kiosk): consent wizard — OTP flow, per-purpose consent, fluid layout, reset-on-done"
```

---

## Notes for the implementer

- **Read before writing:** `admin-bff/pkg/handlers/proxy.go`, `admin-bff/cmd/server/main.go`, and `admin-bff/bootstrap/env.go` are the templates for the Go side; `frontend/admin-dashboard/` (esp. `src/api/client.ts`, `vite.config.ts`) is the template for the PWA side. Match their style.
- **The BFF talks to services directly** (notification-service:9004, consent-service:9000), exactly like admin-bff — not back through the gateway. The gateway only routes the *public* `/kiosk/*` edge to the BFF.
- **Both downstream routes require the hospital JWT** (`middleware.JWTAuth`), which is the entire reason the BFF exists.
- Out of scope (do not build): offline/service worker, §9 guardian flow, per-device credentials, multi-language, a dynamic notice endpoint. See the spec's out-of-scope list.
