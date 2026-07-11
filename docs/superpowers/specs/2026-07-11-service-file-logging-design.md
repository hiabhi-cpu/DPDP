# Unified file logging across all services

**Date:** 2026-07-11
**Status:** Approved

## Goal

Every Go service writes structured logs to the host directory `/data/logs`, laid out as:

```
/data/logs/<service>/<yyyy-mm-dd>/app.log   # application (logrus) log
/data/logs/<service>/<yyyy-mm-dd>/gin.log   # gin access/error log
```

Requirements:
1. Each service has its own folder under `/data/logs`.
2. Inside each service, a folder per day named `yyyy-mm-dd`, containing `app.log` and `gin.log`.
3. On container restart, the current-day logs are rotated (renamed) so they are not overwritten.
4. Size-based rotation within a day (lumberjack, 15 MB per file).

## Non-goals

- Log retention / pruning of old dated folders (kept forever for now; add later if disk pressure appears).
- Centralized log shipping (ELK, Loki, etc.).
- Changing any handler/business behavior.

## Affected services

Six Go services, all sharing the `github.com/hiabhi-cpu/shared` module and an identical
`cmd/main.go` + `bootstrap/` + `pkg/` layout, all using `gin.New()` + `gin.Logger()` and Go's
stdlib `log`:

- audit-service
- auth-service
- consent-service
- notification-service
- emergency-service
- admin-bff (server entrypoint is `cmd/server/main.go`)

## Design

### 1. Shared package: `shared/logging/logging.go`

Adapted from the reference logging utility. Changes from the reference:

- **No `UtilsStruct` receiver.** Expose a plain package function:
  ```go
  func Setup(serviceName string)
  ```
- **Explicit base dir, no cwd guessing.** Drop `ResolveLogBaseDir()`. Base dir comes from env
  `LOG_DIR` (default `/data/logs`); the service's log dir is `filepath.Join(LOG_DIR, serviceName)`.
- **Configurable level.** `LOG_LEVEL` env, default `info`. Parsed via `logrus.ParseLevel`; on parse
  error fall back to `info`.

Kept from the reference verbatim (they already implement the required behavior):

- `dailyLumberjackWriter` — one directory per day (`<baseDir>/<yyyy-mm-dd>/`), lumberjack size
  rotation at `logMaxSizeMB = 15`, reopens under the new day's dir when the date changes. Mutex-guarded.
- `rotateAtStartup(baseDir, fileName)` — on startup, if `<baseDir>/<today>/<fileName>` exists, rename
  it to `<base>-restarted-<yyyy-mm-ddTHH-MM-SS.mmm><ext>`. This gives requirement 3 (rotate on restart).
- `customFormatter` — `time / func / file / line / level / msg` line format. Requires
  `logrus.SetReportCaller(true)`.

Writer wiring (matches the reference exactly):

- `log.SetOutput(appWriter)` — **app.log is file-only** (no stdout tee).
- `gin.DefaultWriter = io.MultiWriter(ginWriter, os.Stdout)` and
  `gin.DefaultErrorWriter = io.MultiWriter(ginWriter, os.Stderr)` — gin logs go to **both** gin.log and
  stdout/stderr so `docker logs` stays useful.
- `logrus.SetReportCaller(true)`, `logrus.SetLevel(<from LOG_LEVEL>)`, `logrus.SetFormatter(&customFormatter{})`.

`Setup` creates the service base dir with `os.MkdirAll` and calls `rotateAtStartup` for both `app.log`
and `gin.log` before constructing the writers. On any setup error it logs and returns (service keeps
running, logging to stderr default).

### 2. Per-service wiring

logrus's package-level API (`Printf`, `Println`, `Print`, `Fatalf`, `Fatal`, `Panic`, ...) is a drop-in
for stdlib `log`. Migration is therefore an **import-line swap**, no call-site edits:

```go
// before
import "log"
// after
import log "github.com/sirupsen/logrus"
```

Applied to the 13 files that import stdlib `log`:

- `admin-bff/cmd/server/main.go`, `admin-bff/cmd/seedadmin/main.go`, `admin-bff/pkg/handlers/proxy.go`
- `audit-service/cmd/main.go`, `audit-service/pkg/audit/controller/audit_handler.go`
- `auth-service/cmd/main.go`, `auth-service/pkg/auth/service/auth_service.go`
- `consent-service/cmd/main.go`, `consent-service/pkg/consent/outbox/relay.go`
- `emergency-service/cmd/main.go`, `emergency-service/pkg/emergency/outbox/relay.go`
- `notification-service/cmd/main.go`, `notification-service/pkg/otp/service/sms_client.go`

In the **6 server mains**, add as the first statement of `main()` (before `gin.New()` / `gin.Logger()`
so gin captures the configured writers):

```go
logging.Setup("audit-service")   // service-specific name
```

`admin-bff/cmd/seedadmin/main.go` (one-shot CLI seeder) gets **only** the import swap — no `Setup`
call; it logs to the logrus default (stderr).

Service names passed to `Setup`: `audit-service`, `auth-service`, `consent-service`,
`notification-service`, `emergency-service`, `admin-bff`.

### 3. Docker

Each `docker-compose.yml` gains a bind mount (append to an existing `volumes:` block where present,
e.g. audit-service already mounts keys):

```yaml
    volumes:
      - /data/logs:/data/logs
```

No Dockerfile change: containers already run as non-root `appuser` (UID 1000); the runtime
`os.MkdirAll` creates `<service>/<date>` subdirs under the mounted root.

One-time host setup (documented in `DOCKER.md`): the bind-mount root must be writable by UID 1000:

```sh
sudo mkdir -p /data/logs
sudo chown -R 1000:1000 /data/logs
```

Restart rotation needs no extra config: `restart: unless-stopped` restarts re-run `main()` →
`Setup()` → `rotateAtStartup()`.

### 4. Dependencies

Add to `shared/go.mod`:

- `github.com/sirupsen/logrus`
- `gopkg.in/natefinch/lumberjack.v2 v2.0.0`

Then `go mod tidy` in `shared` and in each of the six service modules (go.work resolves the local
`shared`). Each service module that now transitively imports logrus/lumberjack picks up the require +
go.sum entries.

## Verification

- `go build ./...` (via go.work) succeeds for all modules.
- Run one service locally with `LOG_DIR=<tmp>`; confirm `<tmp>/<service>/<today>/app.log` and `gin.log`
  are created and receive lines; hit an endpoint and confirm a gin access line in `gin.log`.
- Restart the process; confirm the previous `app.log`/`gin.log` are renamed to `*-restarted-*` and fresh
  files start.
- `docker compose up` for one service with `/data/logs` chowned to 1000; confirm files land on the host.

## Risks / notes

- `LOG_LEVEL=info` hides the reference's trace output by default; set `LOG_LEVEL=trace` in dev.
- If `/data/logs` is not writable by UID 1000, `Setup` logs an error and the service falls back to
  stderr — services still run, but no file logs. The chown step is required.
