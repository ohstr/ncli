import { lazy, Suspense, useEffect, useMemo, useState, useSyncExternalStore } from "react";
import { api, canRenew, onSession, renew, setCsrf, setRenewer, type Point, type Session } from "./api";
import { Indexes } from "./Indexes";
import { versionLabel } from "./indexes";
import { Chart, type Line } from "./Chart";
import { npubEncode } from "nostr-tools/nip19";
import { identityLabel, type Unit } from "./format";

function npubOf(pubkey: string): string {
  try {
    return npubEncode(pubkey);
  } catch {
    return pubkey;
  }
}
import { EOSE_P99, Kpis, OK_P99 } from "./Stats";
import type { Thresholds } from "./stats";
import { MetricsStore } from "./store";
import mark from "./brand/ncli-mark.svg";
import { applyTheme, savedTheme, type Theme } from "./theme";
import { historyStart, RANGES } from "./window";
import { load, save } from "./persist";
import { parse, serialize } from "./urlstate";

// The sign-in screen carries the signing libraries; a signed-in dashboard
// never loads them.
const Login = lazy(() => import("./Login").then((m) => ({ default: m.Login })));

// ---- chart specs: module-level so charts never rebuild on a React render.

const fixed = (lines: Line[]) => () => lines;

// seen lists a prefix's series that have counted anything, so a legend shows
// the reasons that actually happened rather than every possible one.
const seen =
  (prefix: string, label: (rest: string) => string = (r) => r, limit = 8) =>
  (s: MetricsStore): Line[] => {
    const totals = s.latest?.totals ?? {};
    return s
      .keys(prefix)
      .filter((k) => (totals[k] ?? 0) > 0)
      .sort((a, b) => (totals[b] ?? 0) - (totals[a] ?? 0))
      .slice(0, limit)
      .map((k) => ({ key: k, label: label(k.slice(prefix.length)) }));
  };

const latency = (base: string) =>
  fixed([
    { key: `${base}.p50`, label: "p50", scale: 1000 },
    { key: `${base}.p95`, label: "p95", scale: 1000 },
    { key: `${base}.p99`, label: "p99", scale: 1000 },
  ]);

const CHARTS = {
  messages: fixed(["EVENT", "REQ", "CLOSE", "COUNT", "AUTH", "NEG"].map((t) => ({ key: `messages_total:${t}`, label: t }))),
  events: fixed(["accepted", "duplicate", "rejected", "command"].map((r) => ({ key: `events_total:${r}`, label: r }))),
  eventOK: latency("event_ok_seconds"),
  eose: latency("req_eose_seconds"),
  store: fixed([
    { key: "store_batch_seconds.p50", label: "write p50", scale: 1000 },
    { key: "store_batch_seconds.p99", label: "write p99", scale: 1000 },
    { key: "count_seconds.p99", label: "COUNT p99", scale: 1000 },
    { key: "ws_send_seconds.p99", label: "send p99", scale: 1000 },
  ]),
  rejected: seen("events_rejected_total:"),
  classes: seen("events_by_class_total:"),
  connections: fixed([
    { key: "ws_sessions", label: "sessions" },
    { key: "subscriptions_active", label: "subscriptions" },
  ]),
  churn: fixed([
    { key: "ws_sessions_total", label: "connects" },
    { key: "ws_send_errors_total", label: "send errors" },
    { key: "ws_slow_sends_total", label: "slow sends" },
    { key: "subscriptions_limit_hits_total", label: "sub limit hits" },
  ]),
  storeQueues: fixed([
    { key: "store_task_queue_depth", label: "write queue" },
    { key: "scans_inflight", label: "scans in flight" },
    { key: "store_read_txs_open", label: "read txs" },
  ]),
  backpressure: fixed([
    { key: "scan_busy_total", label: "scan busy" },
    { key: "store_batch_errors_total", label: "write errors" },
  ]),
  workers: fixed([
    { key: "verify_queue_depth", label: "profile checks" },
    { key: "index_behind:search", label: "search behind" },
    { key: "index_queued:search", label: "search queued" },
    { key: "index_behind:zaps", label: "zaps behind" },
    { key: "index_behind:stats", label: "stats behind" },
  ]),
  refusedReads: seen("reads_refused_total:"),
  auth: (s: MetricsStore) => [
    { key: "nip42_auth_accepted_total", label: "AUTH ok" },
    ...seen("nip42_auth_rejected_total:", (r) => `AUTH ${r}`, 4)(s),
    ...seen("admin_denied_total:", (r) => `admin ${r}`, 4)(s),
  ],
  http: seen("http_requests_total:", (r) => r.replace(":", " ")),
  cpu: fixed([{ key: "cpu_seconds_total", label: "CPU cores" }]),
  memory: fixed([
    { key: "rss_bytes", label: "RSS" },
    { key: "heap_bytes", label: "heap" },
  ]),
  goroutines: fixed([
    { key: "goroutines", label: "goroutines" },
    { key: "open_fds", label: "open fds" },
  ]),
};

type ChartId = keyof typeof CHARTS;

interface PanelSpec {
  id: ChartId;
  title: string;
  unit: Unit;
  desc?: string;
  // w is the width in 24ths (default: the row's); h the plot height in px.
  w?: Width;
  h?: number;
  thresholds?: Thresholds;
}

type Width = 6 | 8 | 12 | 24;

const ROWS: { id: string; title: string; w: Width; panels: PanelSpec[] }[] = [
  {
    id: "traffic",
    title: "Traffic",
    w: 12,
    panels: [
      { id: "messages", title: "Messages", unit: "rate", desc: "Client messages by type." },
      { id: "events", title: "Events", unit: "rate", desc: "EVENT outcomes." },
      { id: "classes", title: "Events by kind", unit: "rate" },
      { id: "rejected", title: "Rejections by reason", unit: "rate" },
    ],
  },
  {
    id: "latency",
    title: "Latency",
    w: 8,
    panels: [
      { id: "eventOK", title: "EVENT → OK", unit: "ms", desc: "Time from an EVENT to its OK; dashed lines are the p99 stat thresholds.", thresholds: OK_P99 },
      { id: "eose", title: "REQ → EOSE", unit: "ms", desc: "Time from a REQ to its EOSE; dashed lines are the p99 stat thresholds.", thresholds: EOSE_P99 },
      { id: "store", title: "Store, COUNT and sends", unit: "ms" },
    ],
  },
  {
    id: "load",
    title: "Connections and load",
    w: 8,
    panels: [
      { id: "connections", title: "Sessions and subscriptions", unit: "count" },
      { id: "churn", title: "Connects and send problems", unit: "rate" },
      { id: "storeQueues", title: "Store queues", unit: "count" },
      { id: "workers", title: "Workers and indexes", unit: "count", desc: "Profile checks queued, and events each index has yet to take in." },
      { id: "backpressure", title: "Backpressure", unit: "rate", desc: "Reads refused and writes that failed." },
      { id: "http", title: "HTTP bridges", unit: "rate" },
    ],
  },
  {
    id: "access",
    title: "Access",
    w: 12,
    panels: [
      { id: "auth", title: "AUTH and admin", unit: "rate" },
      { id: "refusedReads", title: "Refused reads", unit: "rate" },
    ],
  },
  {
    id: "process",
    title: "Process",
    w: 8,
    panels: [
      { id: "cpu", title: "CPU", unit: "cores" },
      { id: "memory", title: "Memory", unit: "bytes" },
      { id: "goroutines", title: "Goroutines and fds", unit: "count" },
    ],
  },
];

// ---- live values

function useLatest(store: MetricsStore): Point | null {
  return useSyncExternalStore(
    (fn) => store.subscribe(fn),
    () => store.latest,
  );
}

// ---- page

function Dashboard({ session, onLogout }: { session: Session; onLogout: () => void }) {
  const store = useMemo(() => new MetricsStore(), []);
  const [live, setLive] = useState(false);
  const [jobs, setJobs] = useState(0);
  // The URL wins over saved preferences, so a link opens what it shows.
  const [url] = useState(() => parse(location.search));
  const [theme, setTheme] = useState<Theme>(() => url.theme ?? savedTheme());
  const [range, setRange] = useState(url.range);
  // until is the newest time shown while paused.
  const [until, setUntil] = useState<number | undefined>(() => (url.paused ? Date.now() / 1000 : undefined));
  const tv = { range, until };
  const [view, setView] = useState<string | null>(url.view);
  const [collapsed, setCollapsed] = useState<string[]>(() => url.collapsed ?? (load("collapsed") ?? "").split(",").filter(Boolean));
  const toggleRow = (id: string) => {
    const next = collapsed.includes(id) ? collapsed.filter((c) => c !== id) : [...collapsed, id];
    setCollapsed(next);
    save("collapsed", next.join(",") || null);
  };

  useEffect(() => {
    const q = serialize(location.search, { range, paused: until != null, theme, view, collapsed });
    history.replaceState(history.state, "", location.pathname + q + location.hash);
  }, [range, until, theme, view, collapsed]);

  useEffect(() => {
    // Esc closes a viewed panel.
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && setView(null);
    addEventListener("keydown", onKey);
    return () => removeEventListener("keydown", onKey);
  }, []);
  const latest = useLatest(store);
  const version = versionLabel(session);
  // Metrics live in the relay's memory, so a restart starts them over.
  const since = useSyncExternalStore(
    (fn) => store.subscribe(fn),
    () => historyStart(store.times, range, until),
  );

  useEffect(() => {
    void store.start(setLive);
    return () => store.stop();
  }, [store]);

  // A dropped stream may be an expired session: asking renews it (when the
  // tab keeps a key) or returns to the sign-in screen.
  useEffect(() => {
    if (live) return;
    const t = setTimeout(() => void api.session().catch(() => {}), 3000);
    return () => clearTimeout(t);
  }, [live]);

  return (
    <div className="page">
      <header className="topbar">
        <img className="brand" src={mark} alt="" width="24" height="24" />
        <nav className="crumbs">
          <span className="muted">ncli</span>
          <span className="muted">/</span>
          <h1>Relay Monitor</h1>
        </nav>
        <span className="spacer" />
        <div className="tools">
          <select aria-label="Time range" value={range} onChange={(e) => setRange(Number(e.target.value))}>
            {RANGES.map(([label, sec]) => (
              <option key={sec} value={sec}>
                Last {label}
              </option>
            ))}
          </select>
          {since != null && (
            <span className="muted history-since" title="The relay keeps metrics in memory; they start over when it restarts.">
              history since {new Date(since * 1000).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}
            </span>
          )}
          <select
            aria-label="Refresh"
            value={until == null ? "live" : "paused"}
            onChange={(e) => setUntil(e.target.value === "paused" ? (store.times.at(-1) ?? Date.now() / 1000) : undefined)}
          >
            <option value="live">Live</option>
            <option value="paused">Paused</option>
          </select>
          <span className={`dot ${live ? "live" : ""}`} title={live ? "live" : "reconnecting"} />
          <span className="muted status">{live ? "live" : "reconnecting…"}</span>
          <select
            aria-label="Theme"
            value={theme}
            onChange={(e) => {
              const t = e.target.value as Theme;
              setTheme(t);
              applyTheme(t);
            }}
          >
            <option value="auto">Auto</option>
            <option value="dark">Dark</option>
            <option value="light">Light</option>
          </select>
          <span className={`muted${session.nip05 ? "" : " mono"}`} title={npubOf(session.pubkey)}>
            {identityLabel(session.pubkey, session.nip05)}
          </span>
          {session.expires_at > 0 && (
            <button onClick={onLogout} className="ghost">
              Sign out
            </button>
          )}
        </div>
      </header>

      <Kpis p={latest} jobs={jobs} store={store} {...tv} />

      <Indexes onRunning={setJobs} viewing={view === "indexes"} onView={setView} />

      {ROWS.map((row) => {
        const shut = collapsed.includes(row.id);
        return (
          <div key={row.id} className="row">
            <h2>
              <button className="row-head" aria-expanded={!shut} onClick={() => toggleRow(row.id)}>
                <span className="chevron" aria-hidden="true">{shut ? "▸" : "▾"}</span>
                {row.title}
                {shut && <span className="count">({row.panels.length} panels)</span>}
              </button>
            </h2>
            {!shut && (
              <div className="grid">
                {row.panels.map((p) => (
                  <Chart key={p.id} store={store} {...tv} w={row.w} {...p} lines={CHARTS[p.id]} viewing={view === p.id} onView={setView} />
                ))}
              </div>
            )}
          </div>
        );
      })}

      {(version.label || version.mismatch) && (
        <footer className="footer">
          {version.label && (
            <span className="muted version" title="The relay's build">
              {version.label}
            </span>
          )}
          {version.mismatch && (
            <span className="badge warn" title="Commands and the page may not match the relay">
              {version.mismatch}
            </span>
          )}
        </footer>
      )}
    </div>
  );
}

// renewLead is how long before expiry a kept key renews the session.
const renewLead = 60;

export function App() {
  const [session, setSession] = useState<Session | null>(null);
  const [checked, setChecked] = useState(false);

  useEffect(() => {
    onSession({
      expired: () => {
        setRenewer(null);
        setSession(null);
      },
      renewed: setSession,
    });
  }, []);

  // Renew a minute before the session ends, while the tab keeps a key.
  useEffect(() => {
    if (!session?.expires_at || !canRenew()) return;
    const ms = Math.max(5, session.expires_at - Date.now() / 1000 - renewLead) * 1000;
    const t = setTimeout(() => void renew().catch(() => setSession(null)), ms);
    return () => clearTimeout(t);
  }, [session]);

  useEffect(() => {
    api
      .session()
      .then((s) => {
        setCsrf(s.csrf);
        setSession(s);
      })
      .catch(() => setSession(null))
      .finally(() => setChecked(true));
  }, []);

  if (!checked) return null;
  if (!session)
    return (
      <Suspense fallback={null}>
        <Login onSession={setSession} />
      </Suspense>
    );
  return (
    <Dashboard
      session={session}
      onLogout={() => {
        setRenewer(null);
        void api.logout().finally(() => setSession(null));
      }}
    />
  );
}
