# Unified File Logging Across All Services — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every Go service writes date-partitioned, size-rotated `app.log` (logrus) and `gin.log` files under `/data/logs/<service>/<yyyy-mm-dd>/`, rotating the current-day files on restart.

**Architecture:** A single reusable `shared/logging` package holds the writer logic (date-dir + lumberjack size rotation + restart rename). Each service calls `logging.Setup("<service>")` once at startup and swaps its stdlib `log` import for the logrus alias (drop-in, no call-site edits). Docker bind-mounts the host `/data/logs` into each container.

**Tech Stack:** Go 1.25, go.work multi-module, logrus, lumberjack.v2 (v2.0.0), gin, Docker Compose.

## Global Constraints

- Shared logging code lives in `shared/logging` (module `github.com/hiabhi-cpu/shared`), imported as `github.com/hiabhi-cpu/shared/logging`.
- lumberjack pinned to `gopkg.in/natefinch/lumberjack.v2 v2.0.0`.
- `LOG_DIR` env, default `/data/logs`. `LOG_LEVEL` env, default `info`.
- `app.log` is **file-only** (no stdout). `gin.log` tees to gin.log **and** stdout/stderr.
- Per-day size rotation at 15 MB (`logMaxSizeMB = 15`), `Compress: false`.
- Containers run as non-root `appuser` (UID 1000); the host `/data/logs` must be chowned to `1000:1000`.
- The six in-scope services: `audit-service`, `auth-service`, `consent-service`, `notification-service`, `emergency-service`, `admin-bff`. `gateway` (Caddy) and `DPDP` (infra) are out of scope.
- `logging.Setup(...)` MUST be the first statement in `main()`, before `gin.New()` / `gin.Logger()`, because `gin.Logger()` captures `gin.DefaultWriter` at construction time.

---

### Task 1: `shared/logging` package + dependencies + tests

**Files:**
- Create: `shared/logging/logging.go`
- Create: `shared/logging/logging_test.go`
- Modify: `shared/go.mod` (add requires — done via `go get`)

**Interfaces:**
- Produces: `func Setup(serviceName string)` in package `logging` (import path `github.com/hiabhi-cpu/shared/logging`). Configures logrus + gin writers; safe to call once at startup. No return value.
- Internal (white-box tested): `type dailyLumberjackWriter struct{ baseDir, baseName string; ... }` with `Write([]byte) (int, error)`; `func rotateAtStartup(baseDir, fileName string) error`.

- [ ] **Step 1: Add dependencies to the shared module**

Run:
```bash
cd shared
go get github.com/sirupsen/logrus@v1.9.3
go get gopkg.in/natefinch/lumberjack.v2@v2.0.0
```
Expected: `shared/go.mod` gains `github.com/sirupsen/logrus v1.9.3` and `gopkg.in/natefinch/lumberjack.v2 v2.0.0` in a `require` block. (Ignore "no Go files" notices — the package is added next.)

- [ ] **Step 2: Write the failing tests**

Create `shared/logging/logging_test.go`:
```go
package logging

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDailyWriterCreatesDatedFile(t *testing.T) {
	dir := t.TempDir()
	w := &dailyLumberjackWriter{baseDir: dir, baseName: "app.log"}

	if _, err := w.Write([]byte("hello\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	today := time.Now().Format("2006-01-02")
	path := filepath.Join(dir, today, "app.log")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected log file at %s: %v", path, err)
	}
	if string(b) != "hello\n" {
		t.Fatalf("got %q, want %q", b, "hello\n")
	}
}

func TestRotateAtStartupRenamesExisting(t *testing.T) {
	dir := t.TempDir()
	today := time.Now().Format("2006-01-02")
	dayDir := filepath.Join(dir, today)
	if err := os.MkdirAll(dayDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cur := filepath.Join(dayDir, "app.log")
	if err := os.WriteFile(cur, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := rotateAtStartup(dir, "app.log"); err != nil {
		t.Fatalf("rotateAtStartup: %v", err)
	}

	if _, err := os.Stat(cur); !os.IsNotExist(err) {
		t.Fatalf("expected app.log renamed away, stat err=%v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(dayDir, "app-restarted-*.log"))
	if len(matches) != 1 {
		t.Fatalf("expected 1 restarted backup, got %d", len(matches))
	}
}

func TestRotateAtStartupNoFileIsNoOp(t *testing.T) {
	dir := t.TempDir()
	if err := rotateAtStartup(dir, "app.log"); err != nil {
		t.Fatalf("expected nil when file absent, got %v", err)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd shared && go test ./logging/...`
Expected: FAIL — build error, `undefined: dailyLumberjackWriter` / `undefined: rotateAtStartup` (package `logging.go` not written yet).

- [ ] **Step 4: Write the implementation**

Create `shared/logging/logging.go`:
```go
// Package logging configures logrus (app.log) and gin (gin.log) to write
// date-partitioned, size-rotated files under LOG_DIR/<serviceName>.
package logging

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"gopkg.in/natefinch/lumberjack.v2"
)

const logMaxSizeMB = 15

// dailyLumberjackWriter writes to a date-based path (one directory per day) with
// size rotation. When the date changes it closes the current file and opens a
// new one under the new day's directory.
type dailyLumberjackWriter struct {
	baseDir  string
	baseName string
	current  *lumberjack.Logger
	date     string
	mu       sync.Mutex
}

func (w *dailyLumberjackWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	today := time.Now().Format("2006-01-02")
	if w.date != today || w.current == nil {
		if w.current != nil {
			_ = w.current.Close()
		}
		w.date = today

		dayDir := filepath.Join(w.baseDir, today)
		if err := os.MkdirAll(dayDir, os.ModePerm); err != nil {
			return 0, err
		}
		w.current = &lumberjack.Logger{
			Filename: filepath.Join(dayDir, w.baseName),
			MaxSize:  logMaxSizeMB,
			Compress: false,
		}
	}
	return w.current.Write(p)
}

// rotateAtStartup renames today's current log file to a "-restarted-<ts>" backup
// so a restart does not append to / overwrite the pre-restart file. No-op when
// the file does not exist.
func rotateAtStartup(baseDir, fileName string) error {
	today := time.Now().Format("2006-01-02")
	dayDir := filepath.Join(baseDir, today)
	currentLogPath := filepath.Join(dayDir, fileName)

	if _, statErr := os.Stat(currentLogPath); statErr != nil {
		if os.IsNotExist(statErr) {
			return nil
		}
		return statErr
	}

	ext := filepath.Ext(fileName)
	base := strings.TrimSuffix(fileName, ext)
	timestamp := fmt.Sprintf("restarted-%s", time.Now().Format("2006-01-02T15-04-05.000"))
	backupName := fmt.Sprintf("%s-%s%s", base, timestamp, ext)
	backupPath := filepath.Join(dayDir, backupName)

	return os.Rename(currentLogPath, backupPath)
}

// Setup wires logrus (app.log, file-only) and gin (gin.log + stdout/stderr) to
// date-partitioned, size-rotated files under LOG_DIR/<serviceName>. LOG_DIR
// defaults to /data/logs; LOG_LEVEL defaults to info. On any setup error it logs
// and returns so the service keeps running (logging to the logrus default).
func Setup(serviceName string) {
	log.SetReportCaller(true)
	log.SetFormatter(&customFormatter{})

	level, err := log.ParseLevel(os.Getenv("LOG_LEVEL"))
	if err != nil {
		level = log.InfoLevel
	}
	log.SetLevel(level)

	baseDir := os.Getenv("LOG_DIR")
	if baseDir == "" {
		baseDir = "/data/logs"
	}
	logDir := filepath.Join(baseDir, serviceName)

	if err := os.MkdirAll(logDir, os.ModePerm); err != nil {
		log.Errorln("logging.Setup: cannot create log dir:", err)
		return
	}

	if err := rotateAtStartup(logDir, "app.log"); err != nil {
		log.Errorln("logging.Setup: cannot rotate app.log at startup:", err)
		return
	}
	if err := rotateAtStartup(logDir, "gin.log"); err != nil {
		log.Errorln("logging.Setup: cannot rotate gin.log at startup:", err)
		return
	}

	appWriter := &dailyLumberjackWriter{baseDir: logDir, baseName: "app.log"}
	ginWriter := &dailyLumberjackWriter{baseDir: logDir, baseName: "gin.log"}

	gin.DefaultWriter = io.MultiWriter(ginWriter, os.Stdout)
	gin.DefaultErrorWriter = io.MultiWriter(ginWriter, os.Stderr)

	log.SetOutput(appWriter)
}

type customFormatter struct{}

func (f *customFormatter) Format(entry *log.Entry) ([]byte, error) {
	var fn, file string
	var line int
	if entry.Caller != nil {
		fn = entry.Caller.Function
		file = entry.Caller.File
		line = entry.Caller.Line
	}
	return []byte(fmt.Sprintf("time=%q func=%s file=%s line=%d level=%s msg=%q \n",
		entry.Time.Format("2006/01/02 15:04:05.000000000"),
		fn, file, line,
		entry.Level.String(),
		entry.Message,
	)), nil
}
```

Note: the `entry.Caller != nil` guard is a deliberate hardening over the reference — a nil `Caller` (possible for some entries even with `ReportCaller`) would otherwise panic in the formatter.

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd shared && go test ./logging/... -v`
Expected: PASS — `TestDailyWriterCreatesDatedFile`, `TestRotateAtStartupRenamesExisting`, `TestRotateAtStartupNoFileIsNoOp` all OK.

- [ ] **Step 6: Tidy and build the shared module**

Run: `cd shared && go mod tidy && go build ./...`
Expected: no errors; `go.sum` gains logrus + lumberjack entries.

- [ ] **Step 7: Commit**

```bash
git add shared/logging/logging.go shared/logging/logging_test.go shared/go.mod shared/go.sum
git commit -m "feat(logging): shared date-partitioned file logging package"
```

---

### Task 2: Wire all six services (import swap + Setup call)

**Files (import swap `"log"` → `log "github.com/sirupsen/logrus"`):**
- Modify: `audit-service/cmd/main.go`, `audit-service/pkg/audit/controller/audit_handler.go`
- Modify: `auth-service/cmd/main.go`, `auth-service/pkg/auth/service/auth_service.go`
- Modify: `consent-service/cmd/main.go`, `consent-service/pkg/consent/outbox/relay.go`
- Modify: `emergency-service/cmd/main.go`, `emergency-service/pkg/emergency/outbox/relay.go`
- Modify: `notification-service/cmd/main.go`, `notification-service/pkg/otp/service/sms_client.go`
- Modify: `admin-bff/cmd/server/main.go`, `admin-bff/cmd/seedadmin/main.go`, `admin-bff/pkg/handlers/proxy.go`

**Files (also add `logging.Setup(...)` — the six server mains):**
- `audit-service/cmd/main.go` → `logging.Setup("audit-service")`
- `auth-service/cmd/main.go` → `logging.Setup("auth-service")`
- `consent-service/cmd/main.go` → `logging.Setup("consent-service")`
- `emergency-service/cmd/main.go` → `logging.Setup("emergency-service")`
- `notification-service/cmd/main.go` → `logging.Setup("notification-service")`
- `admin-bff/cmd/server/main.go` → `logging.Setup("admin-bff")`

**Interfaces:**
- Consumes: `logging.Setup(serviceName string)` from Task 1.
- Note: `admin-bff/cmd/seedadmin/main.go` gets the import swap **only** — no `Setup` call (one-shot CLI seeder; logs to logrus default stderr).

Because logrus's package-level API (`Print`, `Printf`, `Println`, `Fatal`, `Fatalf`, `Panic`, ...) is call-compatible with stdlib `log`, swapping the import requires **no changes to any `log.*` call site**.

- [ ] **Step 1: Swap the `log` import in all 13 files**

In each file listed above, find the import of the standard library `log` package and replace it with the logrus alias. In a grouped import block it looks like:
```go
import (
	...
	"log"          // <- remove this line
	...
	log "github.com/sirupsen/logrus"   // <- add this line (third-party group)
	...
)
```
For a single-line import `import "log"`, replace with `import log "github.com/sirupsen/logrus"`.

- [ ] **Step 2: Add `logging.Setup(...)` to each of the six server mains**

In each of the six server mains, add the import `"github.com/hiabhi-cpu/shared/logging"` and insert the `Setup` call as the **first statement inside `main()`** (before any other code, and before `gin.New()`/`gin.Logger()`). Example for `audit-service/cmd/main.go`:
```go
func main() {
	logging.Setup("audit-service")

	ctx := context.Background()
	// ... existing body unchanged ...
}
```
Use the matching service name from the "Files" list above for each service. Do **not** add a `Setup` call to `admin-bff/cmd/seedadmin/main.go`.

- [ ] **Step 3: Tidy every service module**

Run (each service module needs logrus/lumberjack in its own go.sum):
```bash
for d in audit-service auth-service consent-service emergency-service notification-service admin-bff; do
  (cd "$d" && go mod tidy) || exit 1
done
```
Expected: each `go.mod`/`go.sum` gains the logrus (+ transitive lumberjack) entries. No errors.

- [ ] **Step 4: Build the whole workspace**

Run: `go build ./...`  (from the repo root; go.work covers all modules)
Expected: PASS — no compile errors. This confirms every `log.*` call still type-checks against logrus and every `Setup` call resolves.

- [ ] **Step 5: Run existing tests to confirm no regressions**

Run: `go test ./...` (from repo root)
Expected: PASS (or unchanged from the pre-change baseline — if a package had no tests it reports `no test files`).

- [ ] **Step 6: Verify file logging end-to-end for one service (manual, host run)**

Run:
```bash
export LOG_DIR=/tmp/dpdp-logs LOG_LEVEL=info
# start one service that boots without external deps if available; otherwise
# rely on the Task 1 unit tests + Task 3 docker verification.
```
Expected (when a service is started): `/tmp/dpdp-logs/<service>/<today>/app.log` and `gin.log` exist and receive lines; restarting the process renames the previous files to `app-restarted-*.log` / `gin-restarted-*.log`.
(If no service boots standalone in this environment, defer live verification to Task 3's docker run — the writer logic itself is already covered by Task 1 unit tests.)

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "feat(logging): route all services' logs through shared file logging"
```

---

### Task 3: Docker bind mounts + host setup docs

**Files:**
- Modify: `audit-service/docker-compose.yml`, `auth-service/docker-compose.yml`, `consent-service/docker-compose.yml`, `notification-service/docker-compose.yml`, `emergency-service/docker-compose.yml` (append a volume line to the existing `volumes:` block)
- Modify: `admin-bff/docker-compose.yml` (add a new `volumes:` block — it has none)
- Modify: `DOCKER.md` (document the one-time host chown)

**Interfaces:**
- Consumes: nothing from earlier tasks (docker/docs only). Independent of Task 2's code, but the end-to-end verification below exercises Task 1 + Task 2 together.

- [ ] **Step 1: Append the log mount to the five services that already have a `volumes:` block**

In each of `audit-service`, `auth-service`, `consent-service`, `notification-service`, `emergency-service` compose files, add `- /data/logs:/data/logs` as the last entry of the service's existing `volumes:` list. Example (`audit-service/docker-compose.yml`):
```yaml
    volumes:
      - ../auth-service/keys:/keys:ro
      - /data/logs:/data/logs
```
`auth-service` uses `./keys:/keys:ro`; `consent-service` and `emergency-service` have two existing entries (`keys` + `secrets`) — append after them; `notification-service` uses `../auth-service/keys:/keys:ro`. Append the same `- /data/logs:/data/logs` line in each.

- [ ] **Step 2: Add a `volumes:` block to admin-bff**

In `admin-bff/docker-compose.yml`, insert a `volumes:` block between the `ports:` and `restart:` lines of the `admin-bff` service:
```yaml
    ports:
      - "9007:9007"
    volumes:
      - /data/logs:/data/logs
    restart: unless-stopped
```

- [ ] **Step 3: Validate every compose file parses**

Run:
```bash
for d in audit-service auth-service consent-service notification-service emergency-service admin-bff; do
  (cd "$d" && docker compose config >/dev/null) && echo "$d OK" || { echo "$d FAILED"; exit 1; }
done
```
Expected: `... OK` for all six. (`docker compose config` fails on malformed YAML/indentation.)

- [ ] **Step 4: Document the one-time host setup in DOCKER.md**

Add a short "Log volume" section to `DOCKER.md` stating that all services bind-mount the host `/data/logs`, that logs are laid out as `/data/logs/<service>/<yyyy-mm-dd>/{app.log,gin.log}`, and that the directory must be writable by the container UID (1000) before first run:
```sh
sudo mkdir -p /data/logs
sudo chown -R 1000:1000 /data/logs
```
Also note the `LOG_LEVEL` env (default `info`; set `LOG_LEVEL=trace` for verbose dev logging) and `LOG_DIR` (default `/data/logs`).

- [ ] **Step 5: End-to-end verification with Docker**

Run (requires the shared infra network per DOCKER.md and the host chown from Step 4):
```bash
sudo mkdir -p /data/logs && sudo chown -R 1000:1000 /data/logs
cd audit-service && docker compose up -d --build
sleep 5
ls -R /data/logs/audit-service
```
Expected: `/data/logs/audit-service/<today>/app.log` and `gin.log` exist on the host and contain lines (app.log has the "listening on" startup line; hit `http://localhost:9001/health` to produce a gin.log access line). Restart (`docker compose restart`) and confirm the previous files are renamed to `*-restarted-*`.

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "chore(docker): bind-mount /data/logs into all services + host setup docs"
```

---

## Self-Review

**Spec coverage:**
- Each service own folder → `LOG_DIR/<serviceName>` in `Setup` (Task 1) + per-service names (Task 2). ✓
- `yyyy-mm-dd` day folders with `app.log`/`gin.log` → `dailyLumberjackWriter` (Task 1). ✓
- Rotate on restart → `rotateAtStartup` called in `Setup` (Task 1), re-run on container restart via `restart: unless-stopped` (Task 3). ✓
- Size rotation → lumberjack `MaxSize: 15` (Task 1). ✓
- app.log file-only, gin.log tees to stdout → writer wiring (Task 1). ✓
- `LOG_LEVEL` default info, `LOG_DIR` default /data/logs → `Setup` (Task 1); documented (Task 3). ✓
- Migrate services to logrus → import swap, 13 files (Task 2). ✓
- Bind mount + chown 1000 → compose edits + DOCKER.md (Task 3). ✓
- shared/logging home + lumberjack v2.0.0 → Task 1 deps. ✓

**Placeholder scan:** No TBD/TODO/"similar to"/"add error handling" — all code and commands are concrete. ✓

**Type consistency:** `Setup(serviceName string)` and `dailyLumberjackWriter`/`rotateAtStartup` names are identical across Task 1 (definition/tests) and Task 2 (call sites). ✓
