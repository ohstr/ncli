import { npubEncode } from "nostr-tools/nip19";

export type Unit = "rate" | "ms" | "count" | "bytes" | "cores" | "percent";

// identityLabel names a signed-in key: its NIP-05 address ("_@domain" shown
// as the domain), else a shortened npub. Never the raw hex.
export function identityLabel(pubkey: string, nip05?: string): string {
  if (nip05) return nip05.startsWith("_@") ? nip05.slice(2) : nip05;
  try {
    const npub = npubEncode(pubkey);
    return `${npub.slice(0, 10)}…${npub.slice(-6)}`;
  } catch {
    return pubkey;
  }
}

function trim(n: number, digits: number): string {
  return n.toFixed(digits).replace(/\.0+$/, "").replace(/(\.\d*?)0+$/, "$1");
}

function si(n: number): string {
  const a = Math.abs(n);
  if (a >= 1e9) return trim(n / 1e9, 1) + "G";
  if (a >= 1e6) return trim(n / 1e6, 1) + "M";
  if (a >= 1e3) return trim(n / 1e3, 1) + "k";
  if (a >= 10 || a === 0) return trim(n, 0);
  return trim(n, a >= 1 ? 1 : 2);
}

export function formatBytes(b: number): string {
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let i = 0;
  while (Math.abs(b) >= 1024 && i < units.length - 1) {
    b /= 1024;
    i++;
  }
  return trim(b, b >= 100 || i === 0 ? 0 : 1) + " " + units[i];
}

export function formatMs(ms: number): string {
  if (ms >= 1000) return trim(ms / 1000, ms >= 10_000 ? 0 : 1) + " s";
  if (ms >= 10) return trim(ms, 0) + " ms";
  if (ms >= 1) return trim(ms, 1) + " ms";
  return trim(ms * 1000, 0) + " µs";
}

export function formatValue(v: number, unit: Unit, axis = false): string {
  switch (unit) {
    case "rate":
      return si(v) + (axis ? "" : "/s");
    case "ms":
      return formatMs(v);
    case "bytes":
      return formatBytes(v);
    case "cores":
      return trim(v, Math.abs(v) >= 1 ? 2 : 3);
    case "percent":
      return trim(v, 1) + "%";
    default:
      return si(v);
  }
}

export function formatUptime(s: number): string {
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (d) return `${d}d ${h}h`;
  if (h) return `${h}h ${m}m`;
  return `${m}m ${Math.floor(s % 60)}s`;
}

export function ago(unix?: number): string {
  if (!unix) return "–";
  const s = Date.now() / 1000 - unix;
  if (s < 60) return "just now";
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  return `${Math.floor(s / 86400)}d ago`;
}
