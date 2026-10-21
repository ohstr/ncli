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
  window_ms: number;
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
  scan_estimate: number;
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

// When the tab still holds a signer (a key sign-in kept for this tab), an
// expired session is renewed without asking; otherwise the app is told the
// session is gone so it can show the sign-in screen.
let renewer: SignFn | null = null;
let release: (() => void) | null = null;
let renewing: Promise<Session> | null = null;
let expired: () => void = () => {};
let renewed: (s: Session) => void = () => {};

// setRenewer keeps sign for renewals; the previous one's key is released.
// setRenewer(null) on sign-out wipes it.
export function setRenewer(sign: SignFn | null, onRelease?: () => void) {
  release?.();
  renewer = sign;
  release = onRelease ?? null;
}

export function canRenew(): boolean {
  return renewer !== null;
}

export function onSession(handlers: { expired: () => void; renewed: (s: Session) => void }) {
  expired = handlers.expired;
  renewed = handlers.renewed;
}

// renew signs in again with the kept signer; concurrent callers share one try.
export function renew(): Promise<Session> {
  if (!renewer) return Promise.reject(new ApiError(401, "session expired"));
  if (!renewing) {
    const sign = renewer;
    renewing = loginWith(sign)
      .then((s) => {
        setCsrf(s.csrf);
        renewed(s);
        return s;
      })
      .finally(() => {
        renewing = null;
      });
  }
  return renewing;
}

async function send(method: string, path: string, body?: unknown, headers: Record<string, string> = {}): Promise<Response> {
  const init: RequestInit = { method, credentials: "same-origin", headers: { ...headers } };
  if (method !== "GET") (init.headers as Record<string, string>)["X-CSRF-Token"] = csrf;
  if (body !== undefined) {
    init.body = JSON.stringify(body);
    (init.headers as Record<string, string>)["Content-Type"] = "application/json";
  }
  return fetch(path, init);
}

async function call<T>(method: string, path: string, body?: unknown, headers: Record<string, string> = {}): Promise<T> {
  let res = await send(method, path, body, headers);
  if (res.status === 401 && path !== "/admin/api/login") {
    if (renewer) {
      try {
        await renew();
        res = await send(method, path, body, headers);
      } catch {
        /* fall through to expiry */
      }
    }
    if (res.status === 401) expired();
  }
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

export interface LoginTemplate {
  kind: number;
  created_at: number;
  tags: string[][];
  content: string;
}

// SignFn signs the login event; whatever holds the key does it (an
// extension, this tab, or a remote signer).
export type SignFn = (ev: LoginTemplate) => Promise<object>;

export async function loginTemplate(): Promise<LoginTemplate> {
  return {
    kind: 27235,
    created_at: Math.floor(Date.now() / 1000),
    tags: [
      ["u", `${location.origin}/admin/api/login`],
      ["method", "POST"],
      ["payload", await sha256Hex("")],
      ["nonce", nonce()],
    ],
    content: "",
  };
}

// loginWith signs one NIP-98 event (kind 27235) for POST /admin/api/login
// and trades it for a session cookie. Only the signed event leaves the tab.
export async function loginWith(sign: SignFn): Promise<Session> {
  const signed = await sign(await loginTemplate());
  const header = "Nostr " + btoa(JSON.stringify(signed));
  return call<Session>("POST", "/admin/api/login", undefined, { Authorization: header });
}

// login signs in with the browser's NIP-07 extension.
export async function login(): Promise<Session> {
  const nostr = (window as unknown as { nostr?: Nip07 }).nostr;
  if (!nostr) throw new Error("No Nostr signer extension (NIP-07) found in this browser.");
  return loginWith((ev) => nostr.signEvent(ev));
}
