# DPDP Consent Manager — Service Architecture & Flow Reference

## Monorepo Overview

```
DPDP/
├── services/           ← 8 independent Go modules
│   ├── auth-service         :9006  Hospital API-key → RS256 JWT
│   ├── consent-service      :9000  Consent capture / check / withdraw
│   ├── audit-service        :9001  Immutable event log
│   ├── notification-service :9004  OTP send & verify (SMS)
│   ├── withdrawal-service   :9002  Withdrawal management (scaffold)
│   ├── report-service       :9005  Compliance reports (scaffold)
│   ├── emergency-service    :9003  Emergency access override (scaffold)
│   └── integration-service  :9007  HMS webhook bridge (scaffold)
├── shared/             ← cross-service libraries (crypto, secrets)
├── frontend/
│   ├── kiosk/          ← React Native / Expo (patient-facing)
│   └── admin/          ← React / Vite (hospital dashboard)
└── docker-compose.yml  ← PostgreSQL 16 + Redis 7
```

**Infrastructure:** Single PostgreSQL database with per-service schemas enforced by Row-Level Security (RLS). Redis for OTP TTL management.

---

## Shared Libraries (`shared/`)

### `shared/crypto`
- `ComputePatientKey(mobile, salt, hospitalKey) string` — HMAC-SHA256 double-keyed hash. The raw mobile number never leaves this function.
- `GenerateOTP() string` — cryptographically random 6-digit code via `crypto/rand`.
- `VerifyAPIKey(raw, hash) bool` — timing-safe bcrypt comparison.

### `shared/secrets`
- `Provider` interface with `GetSystemSalt(ctx)` and `GetHospitalKey(ctx, hospitalID)`.
- `MockProvider` — reads from `secrets/local_hospital_keys.json` for local dev.
- `AWSProvider` — reads from AWS Secrets Manager in production.

---

## 1. auth-service (port 9006)

### Architecture

```
main.go
 └── config.Load()              ← reads .env, parses RSA PEM files
      ├── repository.NewHospitalRepository(pool)  → domain.HospitalRepository
      ├── service.NewAuthService(privKey, pubKey, expiry) → domain.AuthUsecase
      └── handlers.NewAuthHandler(svc, repo)
           └── gin router
                ├── GET  /health
                └── POST /v1/auth/token
```

**Layer dependencies:**
```
handlers → domain ← repository
handlers → domain ← service
```
Neither `repository` nor `service` imports the other — they only see `domain` interfaces.

### Domain Interfaces (`domain/hospital.go`)

| Interface | Methods |
|---|---|
| `HospitalRepository` | `GetByAPIKey(ctx, rawKey)`, `GetByAPIKeyHash(ctx, hash)` |
| `AuthUsecase` | `GetByAPIKey(ctx, rawKey)`, `IssueToken(ctx, id, slug)` |

### Flow: POST /v1/auth/token

```
Client
  │  POST /v1/auth/token  {"api_key": "raw-key"}
  ▼
AuthHandler.IssueToken()
  │  1. Bind & validate request body
  │  2. repo.GetByAPIKey(ctx, rawKey)
  │        ├── SELECT all active hospitals FROM auth.hospitals
  │        └── bcrypt.Compare(rawKey, each hash) until match
  │  3. If nil → 401 "invalid API key"
  │  4. If !hospital.Active → 403 "hospital account is inactive"
  │  5. svc.IssueToken(ctx, hospital.ID, hospital.Slug)
  │        ├── Build HospitalClaims{hospital_id, slug, role, jti, exp}
  │        └── jwt.NewWithClaims(RS256, claims).SignedString(privateKey)
  └── 200 {"token": "eyJ...", "expires_at": "...", "hospital_id": "..."}
```

**JWT Claims structure:**
```json
{
  "hospital_id": "uuid",
  "hospital_slug": "city-general",
  "role": "hospital",
  "sub": "uuid",
  "jti": "uuid",
  "iat": 1234567890,
  "exp": 1234654290
}
```

**Security notes:**
- RS256 asymmetric signing — private key stays in auth-service, public key shared with all services for verification.
- `jti` (JWT ID) is a UUID v4 per token — prevents token replay.
- Bcrypt comparison is done in-process, not via SQL — prevents timing-based enumeration.

---

## 2. consent-service (port 9000)

### Architecture

```
cmd/main.go
 └── bootstrap.App()
      ├── bootstrap.NewEnv()
      ├── bootstrap.NewDatabase()
      └── pkg/routes/routes.go
           ├── GET  /health
           ├── POST /api/consent/v1/capture    (JWT required)
           ├── GET  /api/consent/v1/check      (JWT required)
           ├── POST /api/consent/v1/withdraw   (JWT required)
           └── GET  /api/consent/v1/stats      (JWT required)

pkg/consent/
  ├── model/         ← ConsentArtifact, ConsentStatus, ConsentStats
  ├── repository/    ← interface + pgx impl (RLS transactions)
  │    ├── queries.go   ← all SQL constants
  │    └── consent_repository.go
  ├── service/       ← ConsentService interface + impl
  │    └── consent_service.go (Capture, Check, Withdraw, Stats)
  └── controller/    ← HTTP handlers
```

### Database Schema: `consent.consent_vault`

| Column | Type | Notes |
|---|---|---|
| `id` | UUID | Primary key |
| `hospital_id` | text | RLS isolation key |
| `patient_key` | text | HMAC hash of mobile — never raw |
| `hms_patient_id` | text | Hospital's own patient ID |
| `type` | text | `CONSENT_GIVEN` or `WITHDRAWAL` |
| `status` | text | `ACTIVE` or `WITHDRAWN` |
| `purposes` | jsonb | Array of purpose strings |
| `otp_verified` | bool | Was consent captured with OTP? |
| `artifact_hash` | text | SHA-256 of key fields — tamper-evident |
| `previous_id` | UUID | Links withdrawal to original consent |

### Row-Level Security (RLS)

Every DB transaction opens with:
```sql
SET LOCAL app.hospital_id = '<hospital_id>';
```
A PostgreSQL RLS policy on `consent.consent_vault` enforces:
```sql
WHERE hospital_id = current_setting('app.hospital_id')
```
Hospitals can never read each other's data even if a bug passes the wrong `hospital_id`.

### Flow: POST /api/consent/v1/capture

```
Kiosk (after OTP verified)
  │  POST /api/consent/v1/capture
  │  Headers: Authorization: Bearer <JWT>
  │  Body: {mobile, hms_patient_id, purposes, language, otp_verified: true}
  ▼
JWT middleware → extracts hospital_id from claims
  ▼
ConsentController.Capture()
  ▼
ConsentService.Capture()
  │  1. secretsProvider.GetSystemSalt() + GetHospitalKey(hospitalID)
  │  2. patientKey = HMAC(mobile, salt, hospitalKey)  ← mobile discarded
  │  3. repo.FindActiveConsent(hospitalID, patientKey)
  │        └── If found → return existing (idempotent)
  │  4. Compute artifactHash = SHA256(id+hospitalID+patientKey+purposes+time)
  │  5. repo.InsertConsent(artifact)
  │        ├── BEGIN TX
  │        ├── SET LOCAL app.hospital_id = hospitalID  (RLS)
  │        ├── INSERT INTO consent.consent_vault ...
  │        └── COMMIT
  │  6. go logAuditEvent("CONSENT_GRANTED")  ← async goroutine
  └── 201 {artifact_id, status: "ACTIVE", purposes, created_at}
```

### Flow: GET /api/consent/v1/check

```
Doctor's system (HMS)
  │  GET /api/consent/v1/check?hms_patient_id=P123&purpose=TREATMENT
  │  Headers: Authorization: Bearer <JWT>
  ▼
ConsentService.Check()
  │  1. repo.CheckByHMSPatientID(hospitalID, hms_patient_id)
  │        └── SELECT latest ACTIVE CONSENT_GIVEN row
  │  2. If nil → log "CONSENT_MISSING_ACCESS_ATTEMPT", return {allowed: false}
  │  3. Check if requested purpose in consent.Purposes
  │  4. go logAuditEvent("DATA_ACCESSED" or "CONSENT_MISSING_ACCESS_ATTEMPT")
  └── 200 {allowed: true/false, consent_id, purposes, captured_at}
```

### Flow: POST /api/consent/v1/withdraw

```
Patient (via kiosk or portal)
  │  POST /api/consent/v1/withdraw
  │  Body: {mobile, hms_patient_id, purposes: []}
  ▼
ConsentService.Withdraw()
  │  1. Hash mobile → patientKey
  │  2. Find existing active consent (for previous_id link)
  │  3. Insert WITHDRAWAL artifact (type="WITHDRAWAL", status="WITHDRAWN")
  │        └── previous_id = original consent's ID (creates audit chain)
  │  4. go logAuditEvent("CONSENT_WITHDRAWN")
  └── 200 {withdrawn: true}
```

---

## 3. audit-service (port 9001)

### Architecture

```
cmd/main.go
 └── bootstrap.App()
      └── pkg/routes/routes.go
           ├── GET  /health
           ├── POST /internal/audit/log     (no JWT — service-to-service only)
           └── GET  /api/audit/v1/logs      (JWT required)

pkg/audit/
  ├── model/       ← AuditEvent, AuditLogPage, EventType, ActorType
  ├── repository/  ← AuditRepository interface + pgx impl
  │    ├── queries.go
  │    └── audit_repository.go (RLS scoped writes + paginated reads)
  ├── service/     ← AuditService interface (LogEvent, GetLogs)
  └── controller/  ← Log() handler, GetLogs() handler
```

### Database Schema: `audit.events`

| Column | Type | Notes |
|---|---|---|
| `id` | UUID | Primary key |
| `hospital_id` | text | RLS isolation key |
| `event_type` | text | `CONSENT_GRANTED`, `DATA_ACCESSED`, etc. |
| `actor_id` | text | Doctor/kiosk ID |
| `actor_type` | text | `KIOSK`, `DOCTOR`, `PATIENT`, `SYSTEM` |
| `patient_key` | text | Hashed — never raw mobile |
| `consent_id` | UUID | Links event to a consent artifact |
| `request_id` | UUID | Correlates events across services |
| `ip_address` | text | Source IP |
| `details` | jsonb | Event-specific metadata |
| `created_at` | timestamptz | Immutable — no UPDATE/DELETE allowed |

### Flow: POST /internal/audit/log

```
consent-service (goroutine)
  │  POST http://audit-service:9001/internal/audit/log
  │  Body: {hospital_id, event_type, actor_type, patient_key, ...}
  ▼
AuditController.Log()
  │  1. Bind JSON → AuditEvent struct
  │  2. go AuditService.LogEvent(event)  ← async — returns 202 immediately
  │        ├── BEGIN TX
  │        ├── SET LOCAL app.hospital_id = hospitalID  (RLS)
  │        ├── INSERT INTO audit.events ...
  │        └── COMMIT
  └── 202 Accepted  (caller never blocks on audit)
```

**Design principle:** Audit failure never blocks clinical workflows. The `202 Accepted` response is returned before the goroutine writes to DB.

### Flow: GET /api/audit/v1/logs

```
Admin Dashboard
  │  GET /api/audit/v1/logs?page=1&limit=50&event_type=CONSENT_GRANTED
  │  Headers: Authorization: Bearer <JWT>
  ▼
JWT middleware → hospital_id extracted
  ▼
AuditController.GetLogs()
  │  1. Parse page/limit/event_type query params
  │  2. AuditService.GetLogs(AuditLogFilter{hospitalID, eventType, page, limit})
  │        ├── SET LOCAL app.hospital_id (RLS)
  │        └── SELECT * FROM audit.events WHERE hospital_id = $1 [AND event_type = $2]
  │            ORDER BY created_at DESC LIMIT $3 OFFSET $4
  └── 200 {events: [...], total: N, page: 1, limit: 50}
```

---

## 4. notification-service (port 9004)

### Architecture

```
cmd/main.go
 └── bootstrap.App()   ← PostgreSQL + Redis connections
      └── pkg/routes/routes.go
           ├── GET  /health
           ├── POST /api/otp/v1/send
           └── POST /api/otp/v1/verify

pkg/otp/
  ├── repository/   ← OTPRepository (Redis-backed)
  │    └── redis_store.go
  ├── service/      ← OTPService interface + impl + SMS clients
  │    ├── service.go       (interface)
  │    ├── otp_service.go   (SendOTP, VerifyOTP)
  │    └── sms_client.go    (MSG91Client, MockSMSClient)
  └── controller/   ← SendOTP, VerifyOTP HTTP handlers
```

### Storage Strategy

| Store | What | Why |
|---|---|---|
| **Redis** | OTP hash + attempts + expiry | Fast TTL management, auto-expiry at 5 min |
| **PostgreSQL** | `notification.otp_sessions` | Durability + audit trail |

Redis key format: `otp:{hospital_id}:{mobile_hash}` — raw mobile never stored.

### Flow: POST /api/otp/v1/send

```
Kiosk
  │  POST /api/otp/v1/send
  │  Body: {mobile: "9876543210", hospital_id: "uuid", purpose: "CONSENT"}
  ▼
OTPService.SendOTP()
  │  1. secretsProvider.GetSystemSalt() + GetHospitalKey(hospitalID)
  │  2. mobileHash = HMAC(mobile, salt, hospitalKey)  ← mobile isolated
  │  3. otp = crypto/rand 6-digit number
  │  4. otpHash = bcrypt(otp, cost=10)  ← one-way hash
  │  5. Redis HSET otp:{hospitalID}:{mobileHash}
  │        {otp_hash, attempts:0, purpose, expires_at}
  │     Redis EXPIRE → 300 seconds (5 minutes)
  │  6. Postgres INSERT otp_sessions (non-fatal if fails)
  │  7. smsClient.SendOTP(mobile, otp)  ← raw OTP sent, then discarded
  └── 200 {session_id, expires_at, expires_in: 300}
```

### Flow: POST /api/otp/v1/verify

```
Kiosk
  │  POST /api/otp/v1/verify
  │  Body: {hospital_id, mobile, otp: "482910"}
  ▼
OTPService.VerifyOTP()
  │  1. Recompute mobileHash (same HMAC)
  │  2. Redis HGETALL otp:{hospitalID}:{mobileHash}
  │  3. If nil → {verified: false, "OTP expired or not found"}
  │  4. If attempts >= 3 → {verified: false, locked: true}
  │  5. bcrypt.CompareHashAndPassword(storedHash, providedOTP)
  │        ├── Fail → IncrementAttempts; if now >=3 → lock
  │        └── Pass → Redis DEL key (prevents replay)
  └── 200 {verified: true} or 401/429
```

---

## 5. withdrawal-service (port 9002) — Scaffold

Structured skeleton ready for implementation. Planned domain:

- `POST /api/withdrawal/v1/record` — formal consent withdrawal recording with enhanced audit trail and patient notification triggers.
- Will delegate consent state changes to consent-service and emit audit events.

---

## 6. report-service (port 9005) — Scaffold

Planned domain:
- `GET /api/report/v1/compliance` — DPDP compliance summary per hospital.
- `GET /api/report/v1/consent-export` — paginated CSV/JSON export for regulatory submissions.
- Reads from `audit.events` and `consent.consent_vault` (read-only, RLS enforced).

---

## 7. emergency-service (port 9003) — Scaffold

Planned domain:
- `POST /api/emergency/v1/override` — temporary emergency access with mandatory justification.
- Every use generates a high-priority audit event (`EMERGENCY_ACCESS`).
- Access window is time-limited and auto-expires.

---

## 8. integration-service (port 9007) — Scaffold

Planned domain:
- Webhook receiver for HMS (Hospital Management System) events.
- Translates HMS patient lifecycle events into consent-service calls.
- `POST /api/integration/v1/webhook` — inbound HMS events.

---

## Inter-Service Communication

### Communication Map

```
                    ┌─────────────────────────────────────────────┐
                    │           External Clients                   │
                    │   Kiosk App          Admin Dashboard         │
                    └──────┬───────────────────────┬──────────────┘
                           │                       │
               ┌───────────▼───────┐   ┌───────────▼────────┐
               │  auth-service     │   │   auth-service     │
               │  POST /v1/auth/   │   │  (same — JWT issuer)│
               │  token            │   └───────────┬─────────┘
               └───────────┬───────┘               │ JWT
                           │ JWT                   │
               ┌───────────▼───────────────────────▼────────────┐
               │              consent-service :9000              │
               │  /capture  /check  /withdraw  /stats            │
               └──────────────────┬──────────────────────────────┘
                                  │  HTTP POST (async goroutine)
                                  │  /internal/audit/log
               ┌──────────────────▼──────────────────────────────┐
               │              audit-service :9001                 │
               │  /internal/audit/log   /api/audit/v1/logs        │
               └─────────────────────────────────────────────────┘

               ┌─────────────────────────────────────────────────┐
               │           notification-service :9004             │
               │  /api/otp/v1/send   /api/otp/v1/verify          │
               └──────────────────┬──────────────────────────────┘
                                  │  HTTP (MSG91 API)
                                  ▼
                              SMS Gateway
```

### Protocol Details

| From | To | Endpoint | Auth | Pattern |
|---|---|---|---|---|
| Kiosk / Admin | auth-service | `POST /v1/auth/token` | API Key (body) | Request-Response |
| Kiosk | notification-service | `POST /api/otp/v1/send` | None (hospital_id in body) | Request-Response |
| Kiosk | notification-service | `POST /api/otp/v1/verify` | None | Request-Response |
| Kiosk | consent-service | `POST /api/consent/v1/capture` | JWT Bearer | Request-Response |
| HMS / Admin | consent-service | `GET /api/consent/v1/check` | JWT Bearer | Request-Response |
| consent-service | audit-service | `POST /internal/audit/log` | None (internal network) | Fire-and-Forget |
| Admin Dashboard | audit-service | `GET /api/audit/v1/logs` | JWT Bearer | Request-Response |

---

## End-to-End Patient Consent Flow

```
 STEP 1 — Hospital authenticates (one-time / session)
 ─────────────────────────────────────────────────────
 HMS/Kiosk → auth-service:  POST /v1/auth/token {api_key}
           ← 200 {token: "eyJ..."}   (RS256 JWT, 24h expiry)


 STEP 2 — Patient arrives at kiosk, enters mobile number
 ─────────────────────────────────────────────────────────
 Kiosk → notification-service:  POST /api/otp/v1/send
          {mobile, hospital_id, purpose: "CONSENT"}
       ← 200 {session_id, expires_in: 300}
       → SMS delivered to patient's phone


 STEP 3 — Patient enters OTP on kiosk screen
 ─────────────────────────────────────────────
 Kiosk → notification-service:  POST /api/otp/v1/verify
          {mobile, hospital_id, otp: "482910"}
       ← 200 {verified: true}
       (Redis key deleted — prevents replay)


 STEP 4 — Kiosk captures consent
 ─────────────────────────────────
 Kiosk → consent-service:  POST /api/consent/v1/capture
          Authorization: Bearer <JWT>
          {mobile, hms_patient_id, purposes: ["TREATMENT","RESEARCH"],
           language: "en", otp_verified: true}
       ← 201 {artifact_id, status: "ACTIVE"}

       consent-service (async) → audit-service:
          POST /internal/audit/log {event_type: "CONSENT_GRANTED", ...}
       ← 202 Accepted  (async, non-blocking)


 STEP 5 — Doctor accesses patient data (HMS calls consent gate)
 ───────────────────────────────────────────────────────────────
 HMS → consent-service:  GET /api/consent/v1/check
        Authorization: Bearer <JWT>
        ?hms_patient_id=P123&purpose=TREATMENT
     ← 200 {allowed: true, consent_id, purposes, captured_at}

     consent-service (async) → audit-service:
        POST /internal/audit/log {event_type: "DATA_ACCESSED", actor_id: "DR001"}


 STEP 6 — Patient withdraws consent (optional)
 ───────────────────────────────────────────────
 Kiosk → consent-service:  POST /api/consent/v1/withdraw
          {mobile, hms_patient_id, purposes: []}
       ← 200 {withdrawn: true}

       (withdrawal artifact written with previous_id = original consent ID)
       consent-service (async) → audit-service:
          POST /internal/audit/log {event_type: "CONSENT_WITHDRAWN"}


 STEP 7 — DPO reviews audit trail (dashboard)
 ──────────────────────────────────────────────
 Admin → audit-service:  GET /api/audit/v1/logs?event_type=DATA_ACCESSED
          Authorization: Bearer <JWT>
       ← 200 {events: [...], total: N}
```

---

## Security Architecture Summary

| Control | Implementation |
|---|---|
| **Authentication** | RS256 JWT issued by auth-service; all services verify with public key |
| **Tenant Isolation** | PostgreSQL RLS: `SET LOCAL app.hospital_id` per transaction |
| **Patient Privacy** | Raw mobile number hashed (HMAC-SHA256, double-keyed) immediately on receipt |
| **OTP Security** | bcrypt-hashed before Redis storage; 5-min TTL; max 3 attempts; deleted on use |
| **Audit Immutability** | `audit.events` has no UPDATE/DELETE — append-only by policy |
| **Consent Integrity** | Each artifact has SHA-256 hash of all key fields — tamper-evident |
| **API Key Storage** | bcrypt hash stored in DB; raw key never persisted anywhere |
| **Inter-service** | Internal endpoints (e.g. `/internal/audit/log`) not exposed externally |

---

## Standard Service Layout (all 8 services follow this pattern)

```
<service>/
├── cmd/main.go              ← entry point: bootstrap → router → server
├── bootstrap/
│   ├── app.go              ← Application struct (composition root)
│   ├── env.go              ← env var loading with fail-fast validation
│   ├── database.go         ← pgx pool creation + ping
│   └── redis.go            ← (notification-service only)
└── pkg/
    ├── routes/routes.go    ← central API composition root
    └── <domain>/
        ├── model/          ← data types (no internal imports)
        ├── repository/     ← interface + pgx implementation + queries.go
        ├── service/        ← interface + business logic implementation
        ├── controller/     ← HTTP handlers (depends on service interface only)
        └── routes.go       ← domain wiring: repo→svc→ctrl→routes
```

**Dependency rule:** `controller` → `service interface` → `repository interface` → `model`. No layer imports a layer above it. All cross-layer contracts are interfaces defined in the same domain package.
