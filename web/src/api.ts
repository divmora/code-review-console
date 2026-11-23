// API client for /admin/api. Writes carry the session's CSRF token; a 401
// sends the browser to sign-in and back to the current page.

let csrf = "";

export function setCSRF(token: string) {
  csrf = token;
}

export class APIError extends Error {
  status: number;
  constructor(message: string, status: number) {
    super(message);
    this.status = status;
  }
}

export async function api<T = any>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {};
  if (method !== "GET") headers["X-CSRF-Token"] = csrf;
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const r = await fetch(path, { method, headers, cache: "no-store", body: body === undefined ? undefined : JSON.stringify(body) });
  if (r.status === 401) {
    location.href = "/admin/login?next=" + encodeURIComponent(location.pathname);
    throw new APIError("signed out", 401);
  }
  const data = await r.json().catch(() => ({}));
  if (!r.ok) throw new APIError((data && data.error) || "HTTP " + r.status, r.status);
  // An operator's change that waits for an admin: say so (not as an
  // error), and stop the page from treating it as applied.
  if (r.status === 202 && data && data.pending) {
    window.dispatchEvent(new CustomEvent("crc-notice", { detail: data.message }));
    window.dispatchEvent(new Event("crc-pending"));
    throw new APIError("", 202);
  }
  return data as T;
}

export const get = <T = any>(path: string) => api<T>("GET", path);
export const post = <T = any>(path: string, body?: unknown) => api<T>("POST", path, body);
export const put = <T = any>(path: string, body?: unknown) => api<T>("PUT", path, body);
export const del = <T = any>(path: string) => api<T>("DELETE", path);

// Query string from an object, skipping empty values.
export function qs(params: Record<string, string | number | undefined | null>): string {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== null && v !== "") p.set(k, String(v));
  const s = p.toString();
  return s ? "?" + s : "";
}
