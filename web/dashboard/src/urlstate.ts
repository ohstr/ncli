// Dashboard state in the query string, Grafana-style:
// ?from=now-15m&refresh=paused&theme=dark&view=eose&collapsed=latency,process
import type { Theme } from "./theme";
import { MAX_RANGE } from "./window";

export interface UrlState {
  range: number; // seconds, at most MAX_RANGE
  paused: boolean;
  theme?: Theme;
  view: string | null;
  collapsed?: string[];
}

export const DEFAULT_RANGE = 3600;
const UNIT: Record<string, number> = { s: 1, m: 60, h: 3600 };
// kiosk is gone but still dropped from old links.
const OWN = ["from", "refresh", "theme", "kiosk", "view", "collapsed"];

export function parse(search: string): UrlState {
  const q = new URLSearchParams(search);
  const m = /^now-(\d+)([smh])$/.exec(q.get("from") ?? "");
  const range = m ? Math.min(MAX_RANGE, Math.max(60, Number(m[1]) * UNIT[m[2]])) : DEFAULT_RANGE;
  const theme = q.get("theme");
  const collapsed = q.get("collapsed");
  return {
    range,
    paused: q.get("refresh") === "paused",
    theme: theme === "auto" || theme === "dark" || theme === "light" ? theme : undefined,
    view: q.get("view") || null,
    collapsed: collapsed == null ? undefined : collapsed.split(",").filter(Boolean),
  };
}

function span(sec: number): string {
  return sec % 3600 === 0 ? `${sec / 3600}h` : sec % 60 === 0 ? `${sec / 60}m` : `${sec}s`;
}

// serialize writes s over search, keeping parameters it doesn't own and
// leaving defaults out.
export function serialize(search: string, s: UrlState): string {
  const q = new URLSearchParams(search);
  OWN.forEach((k) => q.delete(k));
  if (s.range !== DEFAULT_RANGE) q.set("from", `now-${span(s.range)}`);
  if (s.paused) q.set("refresh", "paused");
  if (s.theme && s.theme !== "auto") q.set("theme", s.theme);
  if (s.view) q.set("view", s.view);
  if (s.collapsed?.length) q.set("collapsed", s.collapsed.join(","));
  const out = q.toString().replace(/%2C/g, ",");
  return out ? `?${out}` : "";
}
