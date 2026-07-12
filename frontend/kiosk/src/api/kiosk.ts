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
