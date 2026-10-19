// What the homepage reads from the relay serving it: its NIP-11 document,
// and a few events over its own WebSocket.

export interface Limitation {
  max_limit?: number;
  max_message_length?: number;
  max_subscriptions?: number;
  max_indexable_tags?: number;
  auth_required?: boolean;
  membership_required?: boolean;
  min_pow_difficulty?: number;
}

export interface Info {
  name?: string;
  description?: string;
  pubkey?: string;
  contact?: string;
  icon?: string;
  banner?: string;
  software?: string;
  version?: string;
  supported_nips?: (number | string)[];
  limitation?: Limitation;
  nip29?: { subgroups?: boolean };
}

export async function fetchInfo(): Promise<Info> {
  const res = await fetch("/", { headers: { Accept: "application/nostr+json" } });
  if (!res.ok) throw new Error(`NIP-11 request failed (${res.status})`);
  return res.json();
}

// relayURL is this relay's address for Nostr clients.
export function relayURL(loc: { protocol: string; host: string } = location): string {
  return `${loc.protocol === "https:" ? "wss" : "ws"}://${loc.host}`;
}

export interface NostrEvent {
  id: string;
  pubkey: string;
  kind: number;
  created_at: number;
  tags: string[][];
  content: string;
}

export interface Probe {
  latencyMs: number;
  events: NostrEvent[];
}

// query opens the relay's WebSocket, sends one REQ and resolves with the
// events stored before EOSE (or what arrived by the timeout) and how long
// the round trip took.
export function query(url: string, filter: Record<string, unknown>, timeoutMs = 6000): Promise<Probe> {
  return new Promise((resolve, reject) => {
    const started = performance.now();
    const events: NostrEvent[] = [];
    let ws: WebSocket;
    try {
      ws = new WebSocket(url);
    } catch (e) {
      reject(e);
      return;
    }
    const finish = (err?: Error) => {
      clearTimeout(timer);
      try {
        ws.close();
      } catch {
        /* already closed */
      }
      if (err) reject(err);
      else resolve({ latencyMs: Math.round(performance.now() - started), events });
    };
    const timer = setTimeout(() => finish(events.length ? undefined : new Error("relay did not answer")), timeoutMs);
    ws.onopen = () => ws.send(JSON.stringify(["REQ", "home", filter]));
    ws.onerror = () => finish(new Error("could not connect"));
    ws.onmessage = (m) => {
      let msg: unknown;
      try {
        msg = JSON.parse(String(m.data));
      } catch {
        return;
      }
      if (!Array.isArray(msg)) return;
      if (msg[0] === "EVENT" && msg[1] === "home") events.push(msg[2] as NostrEvent);
      else if (msg[0] === "EOSE" || msg[0] === "CLOSED") finish();
    };
  });
}

// safeURL returns u when it is an http(s) URL, else "": NIP-11 and profile
// fields are written by others and must not become javascript: links.
export function safeURL(u: unknown): string {
  if (typeof u !== "string") return "";
  try {
    const p = new URL(u);
    return p.protocol === "https:" || p.protocol === "http:" ? p.toString() : "";
  } catch {
    return "";
  }
}

// iconCandidates lists the hero icon's usable sources in order: the NIP-11
// icon, then the operator's profile picture. The page falls through to the
// next one when an image fails to load, then to a letter placeholder.
export function iconCandidates(icon: unknown, picture: unknown): string[] {
  return [safeURL(icon), safeURL(picture)].filter((u) => u !== "");
}

// isTopLevelNote keeps kind-1 notes that aren't replies (no "e" tag), the
// way a profile's "Notes" tab does.
export function isTopLevelNote(ev: NostrEvent): boolean {
  return ev.kind === 1 && !ev.tags.some((t) => t[0] === "e");
}

export type NotePart = { type: "text" | "link" | "image" | "mention"; value: string };

const NOTE_TOKEN = /(https?:\/\/[^\s<>"]+|nostr:(?:npub|nprofile|note|nevent|naddr)1[02-9ac-hj-np-z]+)/gi;
const IMAGE_EXT = /\.(png|jpe?g|gif|webp|avif)(\?[^\s]*)?$/i;

// noteParts splits a note's content into text, links, inline images and
// nostr: mentions, so it renders as text nodes and never as HTML.
export function noteParts(content: string): NotePart[] {
  const parts: NotePart[] = [];
  let last = 0;
  for (const m of content.matchAll(NOTE_TOKEN)) {
    let token = m[0];
    // Trailing punctuation belongs to the sentence, not the URL.
    const trail = token.match(/[.,;:!?)\]]+$/)?.[0] ?? "";
    token = token.slice(0, token.length - trail.length);
    const start = m.index ?? 0;
    if (start > last) parts.push({ type: "text", value: content.slice(last, start) });
    if (token.startsWith("nostr:")) parts.push({ type: "mention", value: token.slice(6) });
    else if (safeURL(token)) parts.push({ type: IMAGE_EXT.test(new URL(token).pathname) ? "image" : "link", value: safeURL(token) });
    else parts.push({ type: "text", value: token });
    last = start + token.length;
  }
  if (last < content.length) parts.push({ type: "text", value: content.slice(last) });
  return parts;
}

// ago formats a Unix timestamp relative to now, coarse like a feed.
export function ago(ts: number, now = Date.now() / 1000): string {
  const s = Math.max(0, Math.floor(now - ts));
  if (s < 60) return "now";
  if (s < 3600) return `${Math.floor(s / 60)}m`;
  if (s < 86400) return `${Math.floor(s / 3600)}h`;
  if (s < 30 * 86400) return `${Math.floor(s / 86400)}d`;
  return new Date(ts * 1000).toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric" });
}

// shortKey abbreviates a bech32 entity for display: npub1abcd…wxyz.
export function shortKey(s: string): string {
  return s.length > 20 ? `${s.slice(0, 10)}…${s.slice(-4)}` : s;
}

// contactLink turns NIP-11's free-form contact into a link when it is one.
export function contactLink(contact: string): { href: string; label: string } | null {
  const c = contact.trim();
  if (!c) return null;
  if (/^mailto:/i.test(c)) return { href: c, label: c.slice(7) };
  if (/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(c)) return { href: `mailto:${c}`, label: c };
  const url = safeURL(c);
  if (url) return { href: url, label: c };
  if (/^(npub|nprofile)1[02-9ac-hj-np-z]+$/.test(c)) return { href: `https://njump.me/${c}`, label: c };
  return { href: "", label: c };
}
