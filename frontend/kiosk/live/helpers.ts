import { vi } from "vitest";
import { execSync } from "node:child_process";

const BFF = process.env.KIOSK_BFF ?? "http://localhost:9008";

/**
 * Point the app's relative /kiosk/api/... paths at the real kiosk-bff.
 *
 * This is the ONLY thing these tests fake. jsdom serves pages from
 * localhost:3000, so a relative fetch would resolve against the wrong origin.
 * Responses, status codes, and timing all come from the real services — that is
 * the whole point of this suite.
 */
export function pointFetchAtLiveBff(): void {
  const realFetch = globalThis.fetch;
  vi.stubGlobal("fetch", (url: string, init?: RequestInit) =>
    realFetch(url.startsWith("http") ? url : BFF + url, init),
  );
}

export function sh(cmd: string): string {
  return execSync(cmd, { stdio: "pipe" }).toString().trim();
}

/** Block until a service answers /health, or throw after ~30s. */
export function waitHealthy(port: number): void {
  for (let i = 0; i < 30; i++) {
    try {
      sh(`curl -sf -m 2 localhost:${port}/health`);
      return;
    } catch {
      sh("sleep 1");
    }
  }
  throw new Error(`service on :${port} never came healthy`);
}

/**
 * Run `body` with a container stopped, always restarting it afterwards.
 * `stop` refuses connections instantly; `pause` freezes the container so TCP
 * connects and nothing ever answers — the only way to exercise a real hang,
 * and therefore the AbortSignal.timeout path.
 */
export async function withOutage(
  container: string,
  healthPort: number,
  mode: "stop" | "pause",
  body: () => Promise<void>,
): Promise<void> {
  sh(`docker ${mode} ${container}`);
  console.log(`[live] ${container} ${mode === "stop" ? "STOPPED (refuses)" : "PAUSED (hangs)"}`);
  try {
    await body();
  } finally {
    sh(`docker ${mode === "stop" ? "start" : "unpause"} ${container}`);
    waitHealthy(healthPort);
    console.log(`[live] ${container} restored`);
  }
}

/** A staged code from run-live.sh. Fails loudly rather than testing nothing. */
export function code(name: string): string {
  const v = process.env[name];
  if (!v) throw new Error(`${name} not set — run these via 'npm run test:live', not vitest directly`);
  return v;
}
