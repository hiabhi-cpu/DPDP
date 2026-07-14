# Front-desk consent flow — B2 (reception + kiosk UIs) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the two screens of the front-desk consent flow — a reception "consent queue" and a code-only kiosk — on top of the B1 backend.

**Architecture:** kiosk-bff gains one read-and-act endpoint (`claim/resolve` → notification resolve + integration name lookup) and extends its capture proxy to fire best-effort `DONE`; the kiosk PWA drops the walk-in steps and becomes `code → consent → done`; admin-dashboard gains a role-scoped `/reception` queue page. All new cross-service calls reuse the hospital JWT the BFFs already mint.

**Tech Stack:** Go 1.25 + gin (kiosk-bff), React + TypeScript + Vite + Vitest (both frontends), the existing `DataTable`/`AppShell`/`AuthContext` (admin-dashboard) and wizard-step pattern (kiosk).

## Global Constraints

- **Code-only kiosk:** the kiosk's single entry is the 6-digit code; the walk-in `mobile`/`otp` steps and the `sendOtp`/`verifyOtp` calls are removed. kiosk-bff's `/otp/send` + `/otp/verify` routes are removed too (dead once the PWA stops calling them).
- **DONE rows leave the reception queue:** the queue renders only `PENDING` + `CODE_SENT` (filter out `DONE` client-side).
- **kiosk-bff resolve reuses the hospital JWT** (B1's claim endpoints are hospital-JWT gated) — no service-token client.
- **resolve returns `{session_id, mobile, name, hms_patient_id}`** (`hms_patient_id` = the claim `ref`, an opaque non-PII HMS id the browser needs for capture). Name lookup failure is non-fatal → empty `name`.
- **capture body:** `{mobile, session_id, hms_patient_id, purposes}`; on a 201 with `hms_patient_id`, kiosk-bff fires integration `POST /internal/v1/registrations/{hms}/status {status:"DONE"}` best-effort (log on failure, never block the success response).
- **Purposes stay static** — the kiosk uses its existing `notice.ts`; resolve does not return purposes.
- **Role-scoped UI:** `reception` lands on `/reception` and sees only "Consent queue"; admin/dpo never see `/reception`. The BFF is the real gate (403s); the UI just mirrors it.
- Generic kiosk error on a bad/expired code: "Code not recognized — please ask the front desk to resend." No enumeration hint.
- Downstream paths: notification `POST /internal/v1/otp/claim/resolve`; integration `GET /internal/v1/registrations/{ref}` + `POST /internal/v1/registrations/{hms}/status`; consent `POST /api/v1/consent/capture`.

---

### Task 1: kiosk-bff — claim/resolve endpoint + capture-with-DONE

**Files:**
- Create: `kiosk-bff/pkg/handlers/claim.go`
- Test: `kiosk-bff/pkg/handlers/claim_test.go`
- Modify: `kiosk-bff/pkg/routes/routes.go` (Deps + routes; drop the otp routes)
- Modify: `kiosk-bff/bootstrap/env.go` (add IntegrationServiceURL)
- Modify: `kiosk-bff/cmd/server/main.go` (build ClaimHandler, wire Deps)

**Interfaces:**
- Consumes: `hospitaljwt.TokenProvider`.
- Produces: `handlers.NewClaimHandler(notificationBase, integrationBase, consentBase string, token hospitaljwt.TokenProvider) *ClaimHandler` with `Resolve(c *gin.Context)` and `Capture(c *gin.Context)`; routes `POST /kiosk/api/claim/resolve`, `POST /kiosk/api/consent/capture`.

- [ ] **Step 1: Write the failing handler test**

`kiosk-bff/pkg/handlers/claim_test.go`:
```go
package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestResolveCombinesSessionAndName(t *testing.T) {
	gin.SetMode(gin.TestMode)
	notification := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/otp/claim/resolve" {
			t.Errorf("notification path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"session_id":"sess-1","mobile":"9876543210","ref":"PA-1"}`))
	}))
	defer notification.Close()
	integration := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/registrations/PA-1" {
			t.Errorf("integration path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"hms_patient_id":"PA-1","name":"Asha Rao","mobile":"9876543210","status":"CODE_SENT"}`))
	}))
	defer integration.Close()

	h := NewClaimHandler(notification.URL, integration.URL, "http://unused", StubProvider("jwt"))
	r := gin.New()
	r.POST("/kiosk/api/claim/resolve", h.Resolve)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/kiosk/api/claim/resolve", strings.NewReader(`{"otp":"123456"}`)))

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", w.Code, w.Body.String())
	}
	var got map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["session_id"] != "sess-1" || got["mobile"] != "9876543210" || got["name"] != "Asha Rao" || got["hms_patient_id"] != "PA-1" {
		t.Fatalf("resolve body = %s", w.Body.String())
	}
}

func TestResolveFailurePipesError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	notification := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"code not recognized"}`))
	}))
	defer notification.Close()

	h := NewClaimHandler(notification.URL, "http://unused", "http://unused", StubProvider("jwt"))
	r := gin.New()
	r.POST("/kiosk/api/claim/resolve", h.Resolve)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/kiosk/api/claim/resolve", strings.NewReader(`{"otp":"000000"}`)))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", w.Code)
	}
}

func TestResolveNameLookupFailureIsNonFatal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	notification := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"session_id":"sess-1","mobile":"9876543210","ref":"PA-1"}`))
	}))
	defer notification.Close()
	integration := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer integration.Close()

	h := NewClaimHandler(notification.URL, integration.URL, "http://unused", StubProvider("jwt"))
	r := gin.New()
	r.POST("/kiosk/api/claim/resolve", h.Resolve)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/kiosk/api/claim/resolve", strings.NewReader(`{"otp":"123456"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (name lookup failure must not fail resolve)", w.Code)
	}
	var got map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["session_id"] != "sess-1" || got["name"] != "" {
		t.Fatalf("expected empty name fallback, got %s", w.Body.String())
	}
}

func TestCaptureFiresDoneOn201(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var doneCalled bool
	consent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"c-1","hms_patient_id":"PA-1"}`))
	}))
	defer consent.Close()
	integration := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/internal/v1/registrations/PA-1/status" {
			b, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(b), "DONE") {
				t.Errorf("status body = %s", b)
			}
			doneCalled = true
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer integration.Close()

	h := NewClaimHandler("http://unused", integration.URL, consent.URL, StubProvider("jwt"))
	r := gin.New()
	r.POST("/kiosk/api/consent/capture", h.Capture)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/kiosk/api/consent/capture",
		strings.NewReader(`{"mobile":"9876543210","session_id":"sess-1","hms_patient_id":"PA-1","purposes":["treatment"]}`)))

	if w.Code != http.StatusCreated {
		t.Fatalf("code = %d, want 201", w.Code)
	}
	if !doneCalled {
		t.Fatal("expected integration DONE status call after 201 capture")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd kiosk-bff && go test ./pkg/handlers/ -run 'Resolve|Capture' -v`
Expected: FAIL — `NewClaimHandler` undefined.

- [ ] **Step 3: Implement the ClaimHandler**

`kiosk-bff/pkg/handlers/claim.go`:
```go
package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	"github.com/hiabhi-cpu/shared/hospitaljwt"
)

// ClaimHandler drives the code-only kiosk: resolve a code into a verified
// session (+ the patient's name), and forward capture then mark the staged
// record DONE. All downstream calls carry the hospital JWT.
type ClaimHandler struct {
	notificationBase string
	integrationBase  string
	consentBase      string
	token            hospitaljwt.TokenProvider
	client           *http.Client
}

func NewClaimHandler(notificationBase, integrationBase, consentBase string, token hospitaljwt.TokenProvider) *ClaimHandler {
	return &ClaimHandler{
		notificationBase: notificationBase,
		integrationBase:  integrationBase,
		consentBase:      consentBase,
		token:            token,
		client:           &http.Client{Timeout: 10 * time.Second},
	}
}

func (h *ClaimHandler) do(c *gin.Context, method, url string, body []byte) (*http.Response, error) {
	tok, err := h.token.Token(c.Request.Context())
	if err != nil {
		return nil, fmt.Errorf("token: %w", err)
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(c.Request.Context(), method, url, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return h.client.Do(req)
}

// Resolve handles POST /kiosk/api/claim/resolve {otp}. Resolves the code to a
// verified session, looks up the patient's name, returns the identity the
// consent step needs. The raw mobile is included (the walk-in flow already put
// a mobile in the browser; the kiosk resets on done).
func (h *ClaimHandler) Resolve(c *gin.Context) {
	otpBody, err := io.ReadAll(io.LimitReader(c.Request.Body, 4096))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
		return
	}
	resp, err := h.do(c, http.MethodPost, h.notificationBase+"/internal/v1/otp/claim/resolve", otpBody)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "code service unavailable"})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Pipe the generic error (401 "code not recognized" / 429) straight back.
		rb, _ := io.ReadAll(resp.Body)
		ct := resp.Header.Get("Content-Type")
		if ct == "" {
			ct = "application/json"
		}
		c.Data(resp.StatusCode, ct, rb)
		return
	}
	var claim struct {
		SessionID string `json:"session_id"`
		Mobile    string `json:"mobile"`
		Ref       string `json:"ref"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&claim); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "bad code response"})
		return
	}

	// Best-effort name lookup — an outage here must not fail a verified resolve.
	name := ""
	if reg, err := h.do(c, http.MethodGet, h.integrationBase+"/internal/v1/registrations/"+claim.Ref, nil); err == nil {
		if reg.StatusCode == http.StatusOK {
			var r struct {
				Name string `json:"name"`
			}
			if json.NewDecoder(reg.Body).Decode(&r) == nil {
				name = r.Name
			}
		}
		reg.Body.Close()
	}

	c.JSON(http.StatusOK, gin.H{
		"session_id":     claim.SessionID,
		"mobile":         claim.Mobile,
		"name":           name,
		"hms_patient_id": claim.Ref,
	})
}

// Capture handles POST /kiosk/api/consent/capture. Forwards the body to
// consent-service; on a 201 carrying an hms_patient_id, marks the staged record
// DONE (best-effort). Pipes the capture response back either way.
func (h *ClaimHandler) Capture(c *gin.Context) {
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
		return
	}
	resp, err := h.do(c, http.MethodPost, h.consentBase+"/api/v1/consent/capture", body)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "consent service unavailable"})
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusCreated {
		var req struct {
			HMSPatientID string `json:"hms_patient_id"`
		}
		if json.Unmarshal(body, &req) == nil && req.HMSPatientID != "" {
			h.markDone(c, req.HMSPatientID)
		}
	}

	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/json"
	}
	c.Data(resp.StatusCode, ct, respBody)
}

func (h *ClaimHandler) markDone(c *gin.Context, hms string) {
	sr, err := h.do(c, http.MethodPost, h.integrationBase+"/internal/v1/registrations/"+hms+"/status",
		[]byte(`{"status":"DONE"}`))
	if err != nil {
		log.Warnf("kiosk-bff: DONE status update failed for hms=%s: %v", hms, err)
		return
	}
	if sr.StatusCode != http.StatusOK {
		log.Warnf("kiosk-bff: DONE status update returned %d for hms=%s", sr.StatusCode, hms)
	}
	sr.Body.Close()
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd kiosk-bff && go test ./pkg/handlers/ -run 'Resolve|Capture' -v`
Expected: PASS (4 tests).

- [ ] **Step 5: Wire env, routes, main**

In `kiosk-bff/bootstrap/env.go`, add the field + load (mirror `ConsentServiceURL`):
```go
	IntegrationServiceURL  string
```
```go
	IntegrationServiceURL:  getOrDefault("INTEGRATION_SERVICE_URL", "http://localhost:9009"),
```

In `kiosk-bff/pkg/routes/routes.go`, replace `Deps` and the `/kiosk/api` group (drop the otp routes; claim owns resolve + capture):
```go
type Deps struct {
	Claim     *handlers.ClaimHandler
	StaticDir string
}
```
```go
	api := r.Group("/kiosk/api")
	{
		api.POST("/claim/resolve", d.Claim.Resolve)
		api.POST("/consent/capture", d.Claim.Capture)
	}
```

In `kiosk-bff/cmd/server/main.go`, replace the Deps construction:
```go
	routes.Setup(r, routes.Deps{
		Claim: handlers.NewClaimHandler(
			env.NotificationServiceURL, env.IntegrationServiceURL, env.ConsentServiceURL, tokens),
		StaticDir: env.StaticDir,
	})
```
(The `tokens` client is already built above. `handlers.NewProxy` is no longer used by kiosk-bff — leaving the `Proxy` type in the package is fine; the routes test below will not reference it.)

- [ ] **Step 6: Update/trim the routes test**

The existing `kiosk-bff/pkg/routes/routes_test.go` asserts the old otp/consent proxy routes. Replace its route cases so it exercises the new `Deps{Claim: ...}` shape: build a `ClaimHandler` pointed at one fake upstream and assert `/kiosk/api/claim/resolve` and `/kiosk/api/consent/capture` reach it (no `/otp/*`). Minimal replacement:
```go
func newTestRouter(upstreamURL string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	claim := handlers.NewClaimHandler(upstreamURL, upstreamURL, upstreamURL, handlers.StubProvider("test-jwt"))
	Setup(r, Deps{Claim: claim, StaticDir: ""})
	return r
}

func TestRoutesReachUpstream(t *testing.T) {
	var hits []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.URL.Path)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"session_id":"s","mobile":"9876543210","ref":"PA-1"}`))
	}))
	defer upstream.Close()
	r := newTestRouter(upstream.URL)

	for _, p := range []string{"/kiosk/api/claim/resolve", "/kiosk/api/consent/capture"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, p, strings.NewReader(`{"otp":"123456","hms_patient_id":"PA-1"}`)))
		if w.Code == http.StatusNotFound {
			t.Fatalf("%s not routed (404)", p)
		}
	}
	if len(hits) == 0 {
		t.Fatal("no upstream hits")
	}
}
```
Keep the file's existing SPA-fallback/static tests unchanged. Ensure imports include `strings`, `net/http`, `net/http/httptest`, `testing`, `github.com/hiabhi-cpu/kiosk-bff/pkg/handlers`.

- [ ] **Step 7: Build + full kiosk-bff suite**

Run: `cd kiosk-bff && go build ./... && go test ./...`
Expected: build clean; all pass.

- [ ] **Step 8: Commit**

```bash
git add kiosk-bff/
git commit -m "feat(kiosk-bff): claim/resolve (session+name) + capture-with-DONE; drop walk-in otp routes"
```

---

### Task 2: Kiosk PWA — code-only wizard

**Files:**
- Modify: `frontend/kiosk/src/api/kiosk.ts` (add `resolveClaim`, extend `capture`, drop `sendOtp`/`verifyOtp`)
- Create: `frontend/kiosk/src/steps/Code.tsx`
- Delete: `frontend/kiosk/src/steps/Mobile.tsx`, `frontend/kiosk/src/steps/Otp.tsx`
- Modify: `frontend/kiosk/src/steps/Consent.tsx` (greeting)
- Modify: `frontend/kiosk/src/App.tsx` (wizard: code → consent → done)
- Modify: `frontend/kiosk/src/App.test.tsx` (new flow)

**Interfaces:**
- Produces: `resolveClaim(otp) → {session_id, mobile, name, hms_patient_id}`; `capture(mobile, sessionId, purposes, hmsPatientId)`.

- [ ] **Step 1: Update the API module**

In `frontend/kiosk/src/api/kiosk.ts`, remove `sendOtp` and `verifyOtp`, and set:
```ts
export function resolveClaim(
  otp: string,
): Promise<{ session_id: string; mobile: string; name: string; hms_patient_id: string }> {
  return post("/kiosk/api/claim/resolve", { otp });
}

export function capture(
  mobile: string,
  sessionId: string,
  purposes: string[],
  hmsPatientId: string,
): Promise<void> {
  return post("/kiosk/api/consent/capture", {
    mobile,
    session_id: sessionId,
    purposes,
    hms_patient_id: hmsPatientId,
  });
}
```

- [ ] **Step 2: Add the Code step**

`frontend/kiosk/src/steps/Code.tsx`:
```tsx
import { useState } from "react";

export function Code({ busy, error, onSubmit }: {
  busy: boolean;
  error: string;
  onSubmit: (otp: string) => void;
}) {
  const [otp, setOtp] = useState("");
  const valid = /^\d{6}$/.test(otp);
  return (
    <section className="card">
      <h1>Enter your code</h1>
      <p>Type the 6-digit code we just texted you.</p>
      <input
        className="code-input"
        inputMode="numeric"
        pattern="[0-9]*"
        maxLength={6}
        value={otp}
        onChange={(e) => setOtp(e.target.value.replace(/\D/g, "").slice(0, 6))}
        aria-label="6-digit code"
      />
      {error && <p className="error" role="alert">{error}</p>}
      <button className="primary" disabled={busy || !valid} onClick={() => onSubmit(otp)}>
        Continue
      </button>
    </section>
  );
}
```

- [ ] **Step 3: Greet by name in Consent**

In `frontend/kiosk/src/steps/Consent.tsx`, add an optional `name` prop and render a greeting. Change the signature and add the heading:
```tsx
export function Consent({ busy, error, name, onConfirm }: {
  busy: boolean;
  error: string;
  name: string;
  onConfirm: (purposes: string[]) => void;
}) {
```
and immediately inside `<section className="card">`, before `<h2>Consent notice</h2>`:
```tsx
      {name && <p className="greeting">Welcome, {name}</p>}
```

- [ ] **Step 4: Write the failing App test**

The existing `frontend/kiosk/src/App.test.tsx` stubs global `fetch` via a `mockFetchSequence`
helper and has three tests: two walk-in-flow tests (replace these) and a CSS fixed-width
guard (**preserve verbatim** — it reads `globalCss` from `./styles/global.css?raw`). Rewrite
the file to keep the helper + the CSS test and swap the two flow tests for the code-only
flow (still `fetch`-stub style, matching the repo):
```tsx
import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import globalCss from "./styles/global.css?raw";
import { App } from "./App";

afterEach(() => vi.restoreAllMocks());

function mockFetchSequence(responses: Response[]) {
  const fn = vi.fn();
  responses.forEach((r) => fn.mockResolvedValueOnce(r));
  vi.stubGlobal("fetch", fn);
  return fn;
}

describe("code-only kiosk", () => {
  it("enters a code → greets by name → captures with hms_patient_id → done → resets", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const user = userEvent.setup();
    const fetchMock = mockFetchSequence([
      new Response(JSON.stringify({ session_id: "sess-1", mobile: "9876543210", name: "Asha Rao", hms_patient_id: "PA-1" }), { status: 200 }),
      new Response("{}", { status: 201 }),
    ]);

    render(<App />);
    await user.type(screen.getByLabelText(/6-digit code/i), "123456");
    await user.click(screen.getByRole("button", { name: /continue/i }));

    expect(await screen.findByText(/Welcome, Asha Rao/)).toBeInTheDocument();
    await user.click(await screen.findByRole("button", { name: /confirm/i }));
    expect(await screen.findByText(/thank you/i)).toBeInTheDocument();

    // capture posted the hms_patient_id.
    const captureCall = fetchMock.mock.calls.find((c) => String(c[0]).endsWith("/kiosk/api/consent/capture"));
    expect(JSON.parse(captureCall![1].body)).toMatchObject({ session_id: "sess-1", hms_patient_id: "PA-1" });

    vi.advanceTimersByTime(6000);
    await waitFor(() => expect(screen.getByLabelText(/6-digit code/i)).toBeInTheDocument());
    vi.useRealTimers();
  });

  it("shows a generic retry message on a bad code and stays on the code step", async () => {
    const user = userEvent.setup();
    mockFetchSequence([
      new Response(JSON.stringify({ error: "code not recognized" }), { status: 401 }),
    ]);
    render(<App />);
    await user.type(screen.getByLabelText(/6-digit code/i), "000000");
    await user.click(screen.getByRole("button", { name: /continue/i }));
    expect(await screen.findByText(/ask the front desk to resend/i)).toBeInTheDocument();
    expect(screen.getByLabelText(/6-digit code/i)).toBeInTheDocument();
  });

  it("layout uses no fixed pixel widths on the shell/card", () => {
    const widthPx = /(^|[^-])width:\s*\d+px/m.test(globalCss);
    expect(widthPx).toBe(false);
  });
});
```

- [ ] **Step 5: Run the test to verify it fails**

Run: `cd frontend/kiosk && npx vitest run src/App.test.tsx`
Expected: FAIL — App still renders the old welcome/mobile flow; `Code`/`resolveClaim` wiring absent.

- [ ] **Step 6: Rewrite App.tsx to the code-only wizard**

`frontend/kiosk/src/App.tsx`:
```tsx
import { useEffect, useState } from "react";
import { resolveClaim, capture, ApiError } from "./api/kiosk";
import { Code } from "./steps/Code";
import { Consent } from "./steps/Consent";
import { Done } from "./steps/Done";

type Step = "code" | "consent" | "done";

const RESET_MS = 5000;

export function App() {
  const [step, setStep] = useState<Step>("code");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [mobile, setMobile] = useState("");
  const [sessionId, setSessionId] = useState("");
  const [name, setName] = useState("");
  const [hmsPatientId, setHmsPatientId] = useState("");

  function reset() {
    setStep("code");
    setBusy(false);
    setError("");
    setMobile("");
    setSessionId("");
    setName("");
    setHmsPatientId("");
  }

  useEffect(() => {
    if (step !== "done") return;
    const t = setTimeout(reset, RESET_MS);
    return () => clearTimeout(t);
  }, [step]);

  function msg(e: unknown): string {
    return e instanceof ApiError
      ? "Code not recognized — please ask the front desk to resend."
      : "Something went wrong. Please try again.";
  }

  async function onCode(otp: string) {
    setBusy(true);
    setError("");
    try {
      const r = await resolveClaim(otp);
      setSessionId(r.session_id);
      setMobile(r.mobile);
      setName(r.name);
      setHmsPatientId(r.hms_patient_id);
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
      await capture(mobile, sessionId, purposes, hmsPatientId);
      setStep("done");
    } catch (e) {
      setError(msg(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <main className="shell">
      {step === "code" && <Code busy={busy} error={error} onSubmit={onCode} />}
      {step === "consent" && <Consent busy={busy} error={error} name={name} onConfirm={onConfirm} />}
      {step === "done" && <Done />}
    </main>
  );
}
```

- [ ] **Step 7: Update the api unit test, delete dead steps, run tests + typecheck**

`frontend/kiosk/src/api/kiosk.test.ts` currently imports and tests `sendOtp`/`verifyOtp`
(and calls `capture` with the old 3-arg signature) — it will not compile. Rewrite its cases
to the new API (keep the file's existing fetch-stub scaffolding; only the imports + the three
test bodies change):
```ts
import { resolveClaim, capture, ApiError } from "./kiosk";
// ... keep the existing fetch-stub setup ...

it("resolveClaim posts the otp and returns the claim", async () => {
  // stub fetch → 200 {session_id, mobile, name, hms_patient_id}; call resolveClaim("123456");
  // assert the request went to /kiosk/api/claim/resolve with body {otp:"123456"}.
});
it("resolveClaim throws ApiError on a non-2xx", async () => {
  // stub fetch → 401 {error:"code not recognized"}; expect rejects ApiError.
});
it("capture posts session_id + purposes + hms_patient_id", async () => {
  // stub fetch → 201; await capture("9999999999","sess-1",["treatment"],"PA-1");
  // assert url === "/kiosk/api/consent/capture" and body has hms_patient_id "PA-1".
});
```
Then:
```bash
cd frontend/kiosk
rm src/steps/Mobile.tsx src/steps/Otp.tsx
npx vitest run
npx tsc --noEmit
```
Expected: all kiosk tests PASS; no dangling imports of Mobile/Otp/sendOtp/verifyOtp; tsc clean.

- [ ] **Step 8: Add a `.code-input` / `.greeting` style + build**

In `frontend/kiosk/src/styles/global.css`, add (keep the fluid, ≥44px conventions already in the file):
```css
.code-input {
  font-size: clamp(1.5rem, 8vw, 2.5rem);
  letter-spacing: 0.4em;
  text-align: center;
  width: 100%;
  padding: 0.5em;
  min-height: 44px;
}
.greeting { font-weight: 600; margin-bottom: 0.5rem; }
```
Run: `cd frontend/kiosk && npm run build`
Expected: build succeeds.

- [ ] **Step 9: Commit**

```bash
git add frontend/kiosk/
git commit -m "feat(kiosk): code-only wizard — enter code → resolve → consent (greet) → done; drop walk-in steps"
```

---

### Task 3: admin-dashboard — reception queue page

**Files:**
- Modify: `frontend/admin-dashboard/src/api/types.ts` (row type)
- Modify: `frontend/admin-dashboard/src/api/client.ts` (2 calls)
- Create: `frontend/admin-dashboard/src/pages/Reception.tsx`
- Create: `frontend/admin-dashboard/src/pages/Reception.module.css`
- Test: `frontend/admin-dashboard/src/pages/Reception.test.tsx`

**Interfaces:**
- Produces: `PendingRow{hms_patient_id, name, mobile, status, registered_at}`; `api.receptionRegistrations() → PendingRow[]`; `api.sendCode(hms) → {status: string}`; `Reception` page component.

- [ ] **Step 1: Add the type + API calls**

In `frontend/admin-dashboard/src/api/types.ts`, add:
```ts
export interface PendingRow {
  hms_patient_id: string;
  name: string;
  mobile: string; // masked by the server
  status: "PENDING" | "CODE_SENT" | "DONE";
  registered_at: string;
}
```
In `frontend/admin-dashboard/src/api/client.ts`, add to the imported types and the `api` object:
```ts
  receptionRegistrations: () => request<PendingRow[]>("GET", "/api/reception/registrations"),
  sendCode: (hms: string) =>
    request<{ status: string }>("POST", `/api/reception/registrations/${hms}/send-code`),
```
(add `PendingRow` to the `import type { ... } from "./types"` line.)

- [ ] **Step 2: Write the failing page test**

`frontend/admin-dashboard/src/pages/Reception.test.tsx`:
```tsx
import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { Reception } from "./Reception";

vi.mock("../api/client", () => ({
  api: { receptionRegistrations: vi.fn(), sendCode: vi.fn() },
  ApiError: class extends Error {},
}));
import { api } from "../api/client";

const rows = [
  { hms_patient_id: "PA-1", name: "Asha", mobile: "98****3210", status: "PENDING", registered_at: "2026-07-13T10:00:00Z" },
  { hms_patient_id: "PA-2", name: "Ravi", mobile: "97****0009", status: "CODE_SENT", registered_at: "2026-07-13T10:01:00Z" },
  { hms_patient_id: "PA-3", name: "Done Guy", mobile: "96****0000", status: "DONE", registered_at: "2026-07-13T10:02:00Z" },
];

describe("Reception queue", () => {
  beforeEach(() => vi.clearAllMocks());

  it("lists PENDING/CODE_SENT rows, hides DONE, masks mobile, Send vs Resend", async () => {
    (api.receptionRegistrations as any).mockResolvedValue(rows);
    render(<Reception />);
    await waitFor(() => expect(screen.getByText("Asha")).toBeInTheDocument());
    expect(screen.getByText("Ravi")).toBeInTheDocument();
    expect(screen.queryByText("Done Guy")).not.toBeInTheDocument(); // DONE hidden
    expect(screen.getByText("98****3210")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /send code/i })).toBeInTheDocument();  // PENDING
    expect(screen.getByRole("button", { name: /resend/i })).toBeInTheDocument();      // CODE_SENT
  });

  it("calls sendCode when the action is clicked", async () => {
    (api.receptionRegistrations as any).mockResolvedValue([rows[0]]);
    (api.sendCode as any).mockResolvedValue({ status: "sent" });
    render(<Reception />);
    await waitFor(() => expect(screen.getByText("Asha")).toBeInTheDocument());
    fireEvent.click(screen.getByRole("button", { name: /send code/i }));
    await waitFor(() => expect(api.sendCode).toHaveBeenCalledWith("PA-1"));
  });
});
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `cd frontend/admin-dashboard && npx vitest run src/pages/Reception.test.tsx`
Expected: FAIL — `Reception` not found.

- [ ] **Step 4: Implement the page**

`frontend/admin-dashboard/src/pages/Reception.tsx`:
```tsx
import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "../api/client";
import type { PendingRow } from "../api/types";
import { DataTable, type Column } from "../components/DataTable";
import styles from "./Reception.module.css";

const POLL_MS = 5000;

export function Reception() {
  const [rows, setRows] = useState<PendingRow[]>([]);
  const [error, setError] = useState("");
  const [sending, setSending] = useState<Record<string, boolean>>({});

  const load = useCallback(async () => {
    try {
      const all = await api.receptionRegistrations();
      // Completion by disappearance: DONE rows leave the queue.
      setRows(all.filter((r) => r.status !== "DONE"));
      setError("");
    } catch {
      setError("Could not load the queue.");
    }
  }, []);

  useEffect(() => {
    load();
    const t = setInterval(load, POLL_MS);
    return () => clearInterval(t);
  }, [load]);

  async function send(hms: string) {
    setSending((s) => ({ ...s, [hms]: true }));
    try {
      await api.sendCode(hms);
      await load();
    } catch (e) {
      setError(e instanceof ApiError && e.status === 429 ? "Please wait before resending." : "Could not send the code.");
    } finally {
      setSending((s) => ({ ...s, [hms]: false }));
    }
  }

  const columns: Column<PendingRow>[] = [
    { key: "name", header: "Patient", render: (r) => r.name },
    { key: "mobile", header: "Mobile", render: (r) => r.mobile },
    {
      key: "status",
      header: "Status",
      render: (r) => <span className={styles.badge} data-status={r.status}>{r.status === "CODE_SENT" ? "Code sent" : "Awaiting"}</span>,
    },
    {
      key: "action",
      header: "",
      render: (r) => (
        <button className={styles.action} disabled={!!sending[r.hms_patient_id]} onClick={() => send(r.hms_patient_id)}>
          {r.status === "CODE_SENT" ? "Resend" : "Send code"}
        </button>
      ),
    },
  ];

  return (
    <div className={styles.wrap}>
      <h1>Consent queue</h1>
      {error && <p className={styles.error} role="alert">{error}</p>}
      <DataTable columns={columns} rows={rows} empty="No patients awaiting consent." />
    </div>
  );
}
```

`frontend/admin-dashboard/src/pages/Reception.module.css`:
```css
.wrap { padding: 24px; }
.error { color: #b00020; }
.action { padding: 6px 12px; cursor: pointer; }
.badge { padding: 2px 8px; border-radius: 999px; font-size: 0.85em; background: #eee; }
.badge[data-status="CODE_SENT"] { background: #e3f0ff; }
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `cd frontend/admin-dashboard && npx vitest run src/pages/Reception.test.tsx`
Expected: PASS (2 tests).

- [ ] **Step 6: Commit**

```bash
git add frontend/admin-dashboard/src/api/ frontend/admin-dashboard/src/pages/Reception.tsx frontend/admin-dashboard/src/pages/Reception.module.css frontend/admin-dashboard/src/pages/Reception.test.tsx
git commit -m "feat(admin-dashboard): reception consent-queue page (poll, Send/Resend, hide DONE)"
```

---

### Task 4: admin-dashboard — role-scoped routing

**Files:**
- Modify: `frontend/admin-dashboard/src/App.tsx` (add `/reception`, role redirects)
- Modify: `frontend/admin-dashboard/src/components/AppShell.tsx` (nav by role)
- Modify: `frontend/admin-dashboard/src/auth/AuthContext.tsx` (`login` returns `Me`)
- Modify: `frontend/admin-dashboard/src/pages/Login.tsx` (navigate by role)
- Create: `frontend/admin-dashboard/src/auth/roleHome.ts` (helper)
- Test: `frontend/admin-dashboard/src/auth/roleHome.test.ts`

**Interfaces:**
- Consumes: `useAuth().user.role`.
- Produces: `homePathForRole(role: string): string` ("/reception" for reception, "/" otherwise).

- [ ] **Step 1: Write the failing helper test**

`frontend/admin-dashboard/src/auth/roleHome.test.ts`:
```ts
import { describe, it, expect } from "vitest";
import { homePathForRole } from "./roleHome";

describe("homePathForRole", () => {
  it("sends reception to /reception", () => expect(homePathForRole("reception")).toBe("/reception"));
  it("sends admin to /", () => expect(homePathForRole("admin")).toBe("/"));
  it("sends dpo to /", () => expect(homePathForRole("dpo")).toBe("/"));
});
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd frontend/admin-dashboard && npx vitest run src/auth/roleHome.test.ts`
Expected: FAIL — module not found.

- [ ] **Step 3: Implement the helper**

`frontend/admin-dashboard/src/auth/roleHome.ts`:
```ts
// homePathForRole is the landing route for a role. reception is a least-
// privilege operator that only ever sees the consent queue.
export function homePathForRole(role: string): string {
  return role === "reception" ? "/reception" : "/";
}
```

- [ ] **Step 4: Add the route + role redirects in App.tsx**

`frontend/admin-dashboard/src/App.tsx` — add the Reception import, a small `RequireRole` guard, and the `/reception` route; redirect mismatches to the role's home:
```tsx
import { BrowserRouter, Routes, Route, Navigate } from "react-router-dom";
import { AuthProvider, useAuth } from "./auth/AuthContext";
import { ProtectedRoute } from "./components/ProtectedRoute";
import { AppShell } from "./components/AppShell";
import { Login } from "./pages/Login";
import { Dashboard } from "./pages/Dashboard";
import { Audit } from "./pages/Audit";
import { Emergency } from "./pages/Emergency";
import { Reception } from "./pages/Reception";
import { homePathForRole } from "./auth/roleHome";
import { type ReactNode } from "react";

function RequireRole({ roles, children }: { roles: string[]; children: ReactNode }) {
  const { user } = useAuth();
  if (user && !roles.includes(user.role)) return <Navigate to={homePathForRole(user.role)} replace />;
  return <>{children}</>;
}

export default function App() {
  return (
    <AuthProvider>
      <BrowserRouter>
        <Routes>
          <Route path="/login" element={<Login />} />
          <Route element={<ProtectedRoute><AppShell /></ProtectedRoute>}>
            <Route path="/" element={<RequireRole roles={["admin", "dpo"]}><Dashboard /></RequireRole>} />
            <Route path="/audit" element={<RequireRole roles={["admin", "dpo"]}><Audit /></RequireRole>} />
            <Route path="/emergency" element={<RequireRole roles={["admin", "dpo"]}><Emergency /></RequireRole>} />
            <Route path="/reception" element={<RequireRole roles={["reception"]}><Reception /></RequireRole>} />
          </Route>
        </Routes>
      </BrowserRouter>
    </AuthProvider>
  );
}
```

- [ ] **Step 5: Scope the nav by role in AppShell**

In `frontend/admin-dashboard/src/components/AppShell.tsx`, render nav links by role:
```tsx
        <nav className={styles.nav}>
          {user?.role === "reception" ? (
            <NavLink to="/reception" className={cls}>Consent queue</NavLink>
          ) : (
            <>
              <NavLink to="/" end className={cls}>Dashboard</NavLink>
              <NavLink to="/audit" className={cls}>Audit</NavLink>
              <NavLink to="/emergency" className={cls}>Emergency</NavLink>
            </>
          )}
        </nav>
```

- [ ] **Step 6: Redirect to role home after login**

`AuthContext.login` currently returns `void` (it only `setUser(me)`), and `Login.tsx`'s
`onSubmit` does `await login(...); navigate("/", { replace: true })`. Make login return the
`Me` so the caller can branch on role:

In `frontend/admin-dashboard/src/auth/AuthContext.tsx`, change `login` to return the user, and
update the context type to `login: (email: string, password: string) => Promise<Me>`:
```tsx
  const login = async (email: string, password: string) => {
    const me = await api.login(email, password);
    setUser(me);
    return me;
  };
```
In `frontend/admin-dashboard/src/pages/Login.tsx`, import `homePathForRole` from
`../auth/roleHome` and change the post-login navigate:
```tsx
      const me = await login(email, password);
      navigate(homePathForRole(me.role), { replace: true });
```

- [ ] **Step 7: Run helper test + typecheck + build**

```bash
cd frontend/admin-dashboard
npx vitest run src/auth/roleHome.test.ts
npx tsc --noEmit
npm run build
```
Expected: helper test PASS, tsc clean, build succeeds.

- [ ] **Step 8: Commit**

```bash
git add frontend/admin-dashboard/src/App.tsx frontend/admin-dashboard/src/components/AppShell.tsx frontend/admin-dashboard/src/auth/ frontend/admin-dashboard/src/pages/Login.tsx
git commit -m "feat(admin-dashboard): role-scoped routing — reception → /reception, nav + redirects by role"
```

---

### Task 5: Live end-to-end (both UIs)

Drive the whole flow through the two browsers against the running stack, proving the vertical the unit tests can't: a real reception click fires a code, a real kiosk code-entry completes consent, and the row disappears from the queue.

**Files:** none (verification; record the run in the task report).

- [ ] **Step 1: Build the frontends + run the stack**

```bash
cd /home/reddy/Documents/Go/DPDP
# Build both PWAs (or run their Vite dev servers).
(cd frontend/kiosk && npm run build)
(cd frontend/admin-dashboard && npm run build)
# Run: auth, notification, integration, consent, admin-bff, kiosk-bff with their envs
# (INTEGRATION_SERVICE_URL set for kiosk-bff; a reception user seeded — migration 0014 is applied).
```
Confirm each `/health` is ok.

- [ ] **Step 2: Reception fires a code**

Stage a patient via the integration mTLS webhook (CN = hospital_id), then in the admin-dashboard browser (or via the cookie-session API) log in as the reception user, open the consent queue, confirm the staged patient shows with a masked mobile and "Send code", click it → the row flips to "Code sent". Read the OTP from the notification mock-SMS log.

- [ ] **Step 3: Kiosk completes consent**

Open the kiosk PWA, enter the code → the consent screen appears greeting the patient by name → tick purposes → Confirm → Done. Confirm: consent-service wrote a vault row with the `hms_patient_id`; `check` by `hms_patient_id` returns `allowed:true`.

- [ ] **Step 4: The row disappears**

Back on the reception queue (after the ~5s poll), confirm the completed patient is **gone** from the list (DONE filtered). 

- [ ] **Step 5: Record the run**

Write the actual steps + observations into the task report (what verified live: reception send-code → code-entry → consent → done → row disappears → HMS check allowed). No commit unless a fix was needed.

---

## Self-Review

**Spec coverage** (spec section → task):
- kiosk-bff `claim/resolve` (session + name + hms_patient_id) + capture-with-DONE → Task 1.
- Code-only kiosk wizard (remove walk-in), greet by name, capture carries hms_patient_id → Task 2.
- Reception queue page (poll, Send/Resend, mask, hide DONE, empty state) → Task 3.
- Role-scoped routing (reception → /reception, nav + redirects by role) → Task 4.
- Live e2e (send-code → code-entry → consent → done → row disappears) → Task 5.
- Out of scope (guardian, offline, multi-language, resolve-cap refinement) → not touched.

**Placeholder scan:** No TBD/TODO; every code step shows full code, except the kiosk
`api/kiosk.test.ts` rewrite (Task 2 Step 7), which gives the three test cases as comment
scaffolds over the file's existing fetch-stub setup rather than full bodies — deliberate,
since it's a mechanical port of the current file. The three previously-uncertain spots are
now pinned against the real code: `App.test.tsx` keeps its `fetch`-stub style + the CSS-guard
test; `AuthContext.login` is changed to return `Me` (it returned `void`); `Login.tsx`'s
`navigate("/")` becomes `navigate(homePathForRole(me.role))`.

**Type consistency:** `resolveClaim → {session_id, mobile, name, hms_patient_id}` matches the kiosk-bff Resolve JSON (Task 1) and the App state + `capture(mobile, sessionId, purposes, hmsPatientId)` (Task 2). `PendingRow` fields match the admin-bff/integration list shape from B1 (`hms_patient_id, name, mobile, status, registered_at`). `homePathForRole` used identically in App.tsx, Login.tsx, and RequireRole (Task 4). Status strings `PENDING`/`CODE_SENT`/`DONE` verbatim.
