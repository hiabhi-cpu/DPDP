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
