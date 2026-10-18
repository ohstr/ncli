// Admin API client. Same-origin only: the relay (or `ncli relay dashboard`)
// serves this page and the API together, so the session cookie rides along
// and no other origin can call in.

export type Values = Record<string, number>;

export interface Point {
  t: number;
  v: Values;
  totals: Values;
}

export interface Columns {
  interval_ms: number;
  t: number[];
  series: Record<string, (number | null)[]>;
}

export type JobState = "running" | "succeeded" | "failed" | "cancelled";

export interface Job {
  id: string;
  index: string;
  kind: "rebuild" | "drop";
  options: { fresh: boolean; reverify: boolean };
  state: JobState;
  scanned: number;
  indexed: number;
  total?: number;
  started_at: number;
  finished_at?: number;
  error?: string;
}

export interface IndexInfo {
  name: string;
  available: boolean;
  unavailable?: string;
  estimate: number;
  running?: Job;
  last?: Job;
}

export interface Session {
  pubkey: string;
  expires_at: number;
  csrf: string;
}

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}

let csrf = "";

export function setCsrf(token: string) {
  csrf = token;
}

async function call<T>(method: string, path: string, body?: unknown, headers: Record<string, string> = {}): Promise<T> {
  const init: RequestInit = { method, credentials: "same-origin", headers: { ...headers } };
  if (method !== "GET") (init.headers as Record<string, string>)["X-CSRF-Token"] = csrf;
  if (body !== undefined) {
    init.body = JSON.stringify(body);
    (init.headers as Record<string, string>)["Content-Type"] = "application/json";
  }
  const res = await fetch(path, init);
  const text = await res.text();
  if (!res.ok) {
    let msg = text.trim() || res.statusText;
    try {
      msg = JSON.parse(text).error ?? msg;
    } catch {
      /* plain-text error */
    }
    throw new ApiError(res.status, msg);
  }
  return (text ? JSON.parse(text) : undefined) as T;
}

export const api = {
  session: () => call<Session>("GET", "/admin/api/session"),
  logout: () => call<void>("POST", "/admin/api/logout"),
  latest: () => call<Point>("GET", "/admin/api/metrics"),
  history: () => call<Columns>("GET", "/admin/api/history"),
  indexes: () => call<{ indexes: IndexInfo[] }>("GET", "/admin/api/indexes").then((r) => r.indexes ?? []),
  rebuild: (index: string, fresh: boolean, reverify: boolean) =>
    call<Job>("POST", `/admin/api/indexes/${encodeURIComponent(index)}/rebuild`, { fresh, reverify }),
  drop: (index: string) => call<Job>("POST", `/admin/api/indexes/${encodeURIComponent(index)}/drop`),
  cancel: (id: string) => call<Job>("DELETE", `/admin/api/jobs/${encodeURIComponent(id)}`),
};

// NIP-07 browser signer.
interface Nip07 {
  getPublicKey(): Promise<string>;
  signEvent(ev: { kind: number; created_at: number; tags: string[][]; content: string }): Promise<object>;
}

export function hasSigner(): boolean {
  return typeof (window as unknown as { nostr?: Nip07 }).nostr?.signEvent === "function";
}

async function sha256Hex(data: string): Promise<string> {
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(data));
  return [...new Uint8Array(digest)].map((b) => b.toString(16).padStart(2, "0")).join("");
}

function nonce(): string {
  const b = crypto.getRandomValues(new Uint8Array(16));
  return [...b].map((x) => x.toString(16).padStart(2, "0")).join("");
}

// login signs one NIP-98 event (kind 27235) for POST /admin/api/login with
// the browser's signer and trades it for a session cookie.
export async function login(): Promise<Session> {
  const nostr = (window as unknown as { nostr?: Nip07 }).nostr;
  if (!nostr) throw new Error("No Nostr signer extension (NIP-07) found in this browser.");
  const url = `${location.origin}/admin/api/login`;
  const signed = await nostr.signEvent({
    kind: 27235,
    created_at: Math.floor(Date.now() / 1000),
    tags: [
      ["u", url],
      ["method", "POST"],
      ["payload", await sha256Hex("")],
      ["nonce", nonce()],
    ],
    content: "",
  });
  const header = "Nostr " + btoa(JSON.stringify(signed));
  return call<Session>("POST", "/admin/api/login", undefined, { Authorization: header });
}
