export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
    this.name = "ApiError";
  }
}

// fetch has no default timeout: without this a hung connection never settles
// and the capture retry below never gets a chance to fire.
const TIMEOUT_MS = 5000;

const ATTEMPTS = 3;
const RETRY_GAP_MS = 1000;

// A thrown fetch (network down, or the timeout aborting) carries no status and
// is worth another go. Every 4xx is deterministic, and a 500 is a bug, not a
// blip — retrying either just delays an honest answer.
const RETRY_STATUSES = new Set([502, 503, 504]);

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

function retryable(e: unknown): boolean {
  return !(e instanceof ApiError) || RETRY_STATUSES.has(e.status);
}

async function post<T>(path: string, body: unknown): Promise<T> {
  const resp = await fetch(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
    signal: AbortSignal.timeout(TIMEOUT_MS),
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

export function resolveClaim(
  otp: string,
): Promise<{ session_id: string; mobile: string; name: string; hms_patient_id: string }> {
  return post("/kiosk/api/claim/resolve", { otp });
}

export async function capture(
  mobile: string,
  sessionId: string,
  purposes: string[],
  hmsPatientId: string,
): Promise<void> {
  const body = { mobile, session_id: sessionId, purposes, hms_patient_id: hmsPatientId };
  // Retrying is safe because every attempt reuses session_id: consent-service
  // checks the idempotency key before the session verify, so a replay after the
  // write landed returns the original row (200) instead of double-writing.
  // ponytail: the retry lives here, not in post(), so resolveClaim — which is
  // capped per-hospital — cannot retry by construction.
  for (let attempt = 1; ; attempt++) {
    try {
      return await post("/kiosk/api/consent/capture", body);
    } catch (e) {
      if (attempt === ATTEMPTS || !retryable(e)) throw e;
      await sleep(RETRY_GAP_MS);
    }
  }
}
