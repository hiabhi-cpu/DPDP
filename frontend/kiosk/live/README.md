# Kiosk live-stack tests

Drives the real kiosk against the real docker services, with real outages.
**Not** part of `npm test` — these stop, pause, and restart containers.

```bash
cd frontend/kiosk
npm run test:live                 # everything
npm run test:live -- -t "expired" # one test
```

The stack must be up (`RUN_LOCAL.md`) with `SMS_PROVIDER=mock`.

## Why these exist

Every test under `src/` mocks `fetch`. That proves the mapping from an error to
a message, but not that the status a real service emits is the one the mapping
expects — a kiosk-bff returning 500 where we assumed 502 would pass the whole
unit suite and still show a stressed patient the wrong sentence.

Both defects this suite has caught were invisible to unit tests:

- **the 5s `AbortSignal.timeout` was never proven to fire.** Fake timers prove
  the retry loop calls `fetch` three times; only a genuinely hung TCP connection
  proves it ever gives up. `docker pause` produces that — the container freezes,
  TCP connects, nothing answers. Measured: 3 attempts ~6s apart, 17.0s total.
- **a refused and a hung service gave contradictory advice.** Each path passed
  its own unit test; the contradiction only appeared when both were driven
  against the same real service.

## How they work

The only thing faked is the URL origin: jsdom serves from `localhost:3000`, so
the app's relative `/kiosk/api/...` paths need pointing at the real BFF
(`live/helpers.ts`). Responses, status codes, and timing are all real.

`run-live.sh` stages a fresh patient and code per test **immediately** before
vitest starts. This is deliberate — a claim OTP expires after 3 minutes
(`otpExpiry` in `notification-service/pkg/otp/service/otp_service.go`), and
staging earlier makes tests fail at resolve for reasons unrelated to the code
under test. Each patient gets a unique mobile, because `patient_key` is derived
from it and a reused one that already consented would 409.

## Gotchas

- **Not typechecked by `npm run build`.** `tsconfig.app.json` includes only
  `src`, so `live/` is outside it — deliberate, since these need node types the
  app does not. `live/tsconfig.json` exists so editors resolve them.
- **Containers are shared mutable state.** `fileParallelism: false`; don't run
  two of these at once, and don't run them against a stack anyone else is using.
- **Every helper restores its container in a `finally`.** If a run is killed
  mid-test, check `docker ps` — a paused or stopped service will look like a
  broken stack later. `docker unpause dpdp-consent` / `docker start dpdp-consent`.
- **The expired-session test deletes every `session:*` key in Redis.** Fine on a
  dev box; it will sign out anyone else mid-flow.
- **Resolve is capped at 50 per hospital per 3 minutes.** A full run spends 5,
  so back-to-back runs are fine but a tight loop of them is not.
