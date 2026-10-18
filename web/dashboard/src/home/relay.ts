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
