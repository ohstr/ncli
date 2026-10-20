import type { Point } from "./api";
import { formatBytes, formatMs, formatUptime, formatValue } from "./format";
import { level, sparkPath, type Level, type Thresholds } from "./stats";
import type { MetricsStore } from "./store";
import { windowRange } from "./window";

// Latency thresholds in ms, shared with the latency charts.
export const OK_P99: Thresholds = [250, 1000];
export const EOSE_P99: Thresholds = [500, 2000];

interface Ctx {
  n: (k: string) => number;
  totals: Record<string, number>;
  jobs: number;
  live: boolean;
}

interface StatSpec {
  label: string;
  value: (c: Ctx) => number;
  fmt: (v: number) => string;
  sub?: (c: Ctx) => string;
  thresholds?: Thresholds;
  // ready says whether the value means anything yet (no color until then).
  ready?: (c: Ctx) => boolean;
  // spark is the store column drawn under the value.
  spark?: string;
}

const handled = (c: Ctx) => (c.n("events_total:accepted") || 0) + (c.n("events_total:rejected") || 0) + (c.n("events_total:duplicate") || 0);
const ms = (x: number) => formatMs(x);

const STATS: StatSpec[] = [
  { label: "Uptime", value: (c) => (c.live ? c.n("uptime_seconds") : NaN), fmt: formatUptime },
  {
    label: "Sessions",
    value: (c) => c.n("ws_sessions") || 0,
    fmt: (v) => formatValue(v, "count"),
    sub: (c) => `${formatValue(c.n("subscriptions_active") || 0, "count")} subs`,
    spark: "ws_sessions",
  },
  {
    label: "Events accepted",
    value: (c) => c.n("events_total:accepted") || 0,
    fmt: (v) => formatValue(v, "rate"),
    sub: (c) => `${formatValue(c.n("messages_total:REQ") || 0, "rate")} REQ`,
    spark: "events_total:accepted",
  },
  {
    label: "Rejected",
    value: (c) => {
      const h = handled(c);
      return h > 0 ? ((c.n("events_total:rejected") || 0) / h) * 100 : 0;
    },
    fmt: (v) => formatValue(v, "percent"),
    thresholds: [5, 25],
    ready: (c) => handled(c) > 0,
    spark: "events_total:rejected",
  },
  { label: "EVENT→OK p99", value: (c) => c.n("event_ok_seconds.p99") * 1000, fmt: ms, thresholds: OK_P99, spark: "event_ok_seconds.p99" },
  { label: "REQ→EOSE p99", value: (c) => c.n("req_eose_seconds.p99") * 1000, fmt: ms, thresholds: EOSE_P99, spark: "req_eose_seconds.p99" },
  {
    label: "Write queue",
    value: (c) => (c.n("store_task_queue_depth") / (c.n("store_task_queue_capacity") || 1)) * 100,
    fmt: (v) => formatValue(v, "percent"),
    thresholds: [50, 90],
    spark: "store_task_queue_depth",
  },
  { label: "Store", value: (c) => c.n("store_size_bytes"), fmt: formatBytes, spark: "store_size_bytes" },
  {
    label: "CPU",
    value: (c) => c.n("cpu_seconds_total"),
    fmt: (v) => formatValue(v, "cores"),
    sub: (c) => (Number.isFinite(c.n("rss_bytes")) ? formatBytes(c.n("rss_bytes")) : ""),
    spark: "cpu_seconds_total",
  },
  {
    label: "Admin denied",
    value: (c) => Object.entries(c.totals).reduce((a, [k, x]) => (k.startsWith("admin_denied_total:") ? a + x : a), 0),
    fmt: (v) => formatValue(v, "count"),
    thresholds: [1, Infinity],
  },
  { label: "Jobs", value: (c) => c.jobs, fmt: String, sub: (c) => (c.jobs ? "running" : "idle") },
];

// split separates a formatted value from its unit so the unit can be
// smaller; "3m 12s" stays whole.
function split(s: string): [string, string] {
  const m = /^(-?[\d.]+)\s*(\D*)$/.exec(s);
  return m ? [m[1], m[2]] : [s, ""];
}

function Spark({ col }: { col: (number | null)[] }) {
  const d = sparkPath(col, 120, 28);
  if (!d) return null;
  return (
    <svg className="spark" viewBox="0 0 120 28" preserveAspectRatio="none" aria-hidden="true">
      <path d={d} />
    </svg>
  );
}

interface Props {
  p: Point | null;
  jobs: number;
  store: MetricsStore;
  range: number;
  until?: number;
}

// Kpis is the row of Grafana-style stat panels.
export function Kpis({ p, jobs, store, range, until }: Props) {
  const v = p?.v ?? {};
  const c: Ctx = { n: (k) => v[k] ?? NaN, totals: p?.totals ?? {}, jobs, live: p != null };
  const [a, b] = windowRange(store.times, range, until);
  return (
    <div className="kpis">
      {STATS.map((s) => {
        const x = s.value(c);
        const lv: Level = s.thresholds && (!s.ready || s.ready(c)) ? level(x, s.thresholds) : "";
        const [num, unit] = Number.isFinite(x) ? split(s.fmt(x)) : ["–", ""];
        const col = s.spark ? store.get(s.spark).slice(a, b) : [];
        return (
          <div key={s.label} className={`kpi ${lv}`}>
            <span className="kpi-label">{s.label}</span>
            <span className="kpi-value">
              {num}
              {unit && <small>{unit}</small>}
            </span>
            {s.sub && <span className="kpi-sub">{s.sub(c)}</span>}
            {s.spark && <Spark col={col} />}
          </div>
        );
      })}
    </div>
  );
}
