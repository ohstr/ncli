// What the Indexes panel says about an index and its jobs, kept apart from
// the components so it can be tested. Wording matches `ncli relay index`.
import type { IndexInfo, IndexState, Job, JobKind, Plan } from "./api";

// count groups digits: 18 402.
export function count(n: number): string {
  const s = Math.abs(Math.trunc(n)).toString().replace(/\B(?=(\d{3})+(?!\d))/g, " ");
  return n < 0 ? "-" + s : s;
}

export function kindLabel(k: JobKind): string {
  switch (k) {
    case "catch_up":
      return "catch-up";
    case "reap":
      return "free";
  }
  return k;
}

export function stateLabel(s: IndexState): string {
  return s.replace("_", " ");
}

// tone is the badge colour for a state.
export function tone(s: IndexState): "ok" | "warn" | "bad" | "muted" {
  switch (s) {
    case "ready":
      return "ok";
    case "disabled":
    case "empty":
      return "muted";
    case "unreachable":
    case "needs_rebuild":
      return "bad";
  }
  return "warn";
}

// percent is a running job's progress, 0–100, or null when it has no total.
export function percent(j: Job): number | null {
  if (!j.total) return null;
  return Math.min(100, (j.done / j.total) * 100);
}

// eta is the seconds a running job has left, estimated from its pace.
export function eta(j: Job, now: number): number | null {
  if (j.done <= 0 || j.total <= j.done) return null;
  const elapsed = now - j.started_at;
  const left = (elapsed * (j.total - j.done)) / j.done;
  return left >= 1 ? Math.round(left) : null;
}

export function duration(sec: number): string {
  if (sec < 60) return `${Math.round(sec)}s`;
  if (sec < 3600) return `${Math.floor(sec / 60)}m ${Math.round(sec % 60)}s`;
  return `${Math.floor(sec / 3600)}h ${Math.round((sec % 3600) / 60)}m`;
}

// progressText says where a running job is.
export function progressText(j: Job, now: number): string {
  if (j.phase === "waiting") return "waiting for another index job to finish";
  if (j.phase === "cancelling") return "cancelling…";
  if (j.phase?.startsWith("waiting for")) return j.phase;
  const parts: string[] = [];
  if (j.phase && j.phase !== "scan") parts.push(j.phase);
  const pct = percent(j);
  if (pct != null) parts.push(`${Math.round(pct)}%`);
  const left = eta(j, now);
  if (left != null) parts.push(`~${duration(left)} left`);
  if (j.applied > 0) parts.push(`${count(j.applied)} applied`);
  return parts.length ? parts.join(" · ") : "starting";
}

// meta is an index's numbers in a line.
export function meta(ix: IndexInfo, now: number): string {
  const parts: string[] = [];
  if (ix.enabled || ix.has_data) parts.push(`${count(ix.entries)} entries`);
  if (ix.enabled && ix.behind > 0) parts.push(`${count(ix.behind)} events behind`);
  if (ix.queued) parts.push(`${count(ix.queued)} profiles queued`);
  if (ix.built_at) parts.push(`built ${ago(ix.built_at, now)}`);
  return parts.join(" · ");
}

// note says why an idle index isn't simply ready, and what happens next.
export function note(ix: IndexInfo, now: number): string {
  if (ix.state === "unreachable") return `${ix.reason ?? "unreachable"}${ix.error ? ` (${ix.error})` : ""}`;
  if (ix.action && ix.retry_at) return `${kindLabel(ix.action)} needed (${ix.reason}); the last try failed, retrying ${ago(ix.retry_at, now)}`;
  if (ix.action) return `${kindLabel(ix.action)} needed (${ix.reason}); the relay starts it on its own`;
  if (!ix.enabled) return ix.has_data ? `${ix.unavailable}. It still holds data you can drop.` : (ix.unavailable ?? "");
  return "";
}

// ago is a past or future time relative to now (unix seconds).
export function ago(unix: number | undefined, now: number): string {
  if (!unix) return "–";
  const d = now - unix;
  const s = Math.abs(d);
  let t: string;
  if (s < 5) return "just now";
  if (s < 60) t = `${Math.floor(s)}s`;
  else if (s < 3600) t = `${Math.floor(s / 60)}m`;
  else if (s < 172800) t = `${Math.floor(s / 3600)}h`;
  else t = `${Math.floor(s / 86400)}d`;
  return d < 0 ? `in ${t}` : `${t} ago`;
}

// actions are what an admin can start on an idle index: a rebuild needs
// its feature; a drop is for one whose feature is off and that holds data.
export function actions(ix: IndexInfo): { rebuild: boolean; drop: boolean } {
  if (ix.running) return { rebuild: false, drop: false };
  return { rebuild: ix.enabled, drop: !ix.enabled && (ix.has_data || (ix.retired ?? 0) > 0) };
}

// cancellable reports whether an admin can stop a running job: search
// catches up as part of live indexing.
export function cancellable(j: Job): boolean {
  return !(j.index === "search" && j.kind === "catch_up");
}

// planText says what a rebuild or drop will do, before it starts.
export function planText(p: Plan): string {
  if (p.kind === "drop") return `Drop ${count(p.entries)} entries from ${p.index}: ${p.effect}.`;
  const why = p.reason && p.state !== "ready" ? ` (${p.reason})` : "";
  return `Rebuild ${p.index} from ${count(p.scan)} stored events; it holds ${count(p.entries)} entries now${why}. It ${p.effect}.`;
}

// ended lists jobs that were running in before and ended in after, for a
// notice each.
export function ended(before: IndexInfo[], after: IndexInfo[]): Job[] {
  const was = new Set(before.flatMap((ix) => (ix.running?.id ? [ix.running.id] : [])));
  return after.flatMap((ix) => (ix.last?.id && was.has(ix.last.id) && !ix.running ? [ix.last] : []));
}

// versionLabel names the relay's build; mismatch says the CLI serving the
// page is another.
export function versionLabel(s: { version?: { ncli: string; nmilat: string }; client?: { ncli: string; nmilat: string } }): {
  label: string;
  mismatch: string;
} {
  if (!s.version?.ncli) return { label: "", mismatch: "" };
  const label = `ncli ${s.version.ncli} · nmilat ${s.version.nmilat}`;
  const mismatch = s.client && s.client.ncli !== s.version.ncli ? `this CLI is ncli ${s.client.ncli}, the relay runs ${s.version.ncli}` : "";
  return { label, mismatch };
}
