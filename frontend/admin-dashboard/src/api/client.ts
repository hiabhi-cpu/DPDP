import type { ConsentStats, AuditLogPage, EmergencyPending, Me } from "./types";

export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
    this.name = "ApiError";
  }
}

function csrfToken(): string {
  const m = document.cookie.match(/(?:^|;\s*)csrf_token=([^;]+)/);
  return m ? decodeURIComponent(m[1]) : "";
}

type Method = "GET" | "POST" | "DELETE";

async function request<T>(method: Method, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {};
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (method !== "GET") headers["X-CSRF-Token"] = csrfToken();

  const res = await fetch(path, {
    method,
    credentials: "include",
    headers,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });

  if (res.status === 204) return undefined as T;
  const text = await res.text();
  const data = text ? JSON.parse(text) : undefined;
  if (!res.ok) {
    const message = (data && (data.error as string)) || `request failed (${res.status})`;
    throw new ApiError(res.status, message);
  }
  return data as T;
}

export const api = {
  getCsrf: () => request<void>("GET", "/api/csrf"),
  login: (email: string, password: string) =>
    request<Me>("POST", "/api/session", { email, password }),
  logout: () => request<void>("DELETE", "/api/session"),
  me: () => request<Me>("GET", "/api/me"),
  getStats: (windowDays: number) =>
    request<ConsentStats>("GET", `/api/consent/stats?window_days=${windowDays}`),
  getAuditLogs: (params: { page: number; limit: number; event_type?: string }) => {
    const q = new URLSearchParams({ page: String(params.page), limit: String(params.limit) });
    if (params.event_type) q.set("event_type", params.event_type);
    return request<AuditLogPage>("GET", `/api/audit/logs?${q.toString()}`);
  },
  getEmergencyPending: () => request<EmergencyPending>("GET", "/api/emergency/pending"),
  reviewEmergency: (id: string, decision: "VERIFIED" | "FLAGGED") =>
    request<{ status: string }>("POST", `/api/emergency/${id}/review`, { decision }),
};
