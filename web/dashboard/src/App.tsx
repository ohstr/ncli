import { lazy, Suspense, useCallback, useEffect, useMemo, useState, useSyncExternalStore } from "react";
import { api, canRenew, onSession, renew, setCsrf, setRenewer, type IndexInfo, type Job, type Point, type Session } from "./api";
import { Chart, type Line } from "./Chart";
import { Panel } from "./Panel";
import { ago, formatValue, type Unit } from "./format";
import { EOSE_P99, Kpis, OK_P99 } from "./Stats";
import type { Thresholds } from "./stats";
import { MetricsStore } from "./store";
import mark from "./brand/ncli-mark.svg";
import { applyTheme, savedTheme, type Theme } from "./theme";
import { RANGES } from "./window";
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
    { key: "verify_dropped_total", label: "verify dropped" },
    { key: "search_index_dropped_total", label: "search dropped" },
    { key: "store_batch_errors_total", label: "write errors" },
  ]),
  workers: fixed([
    { key: "verify_queue_depth", label: "verify queue" },
    { key: "search_index_queue_depth", label: "search queue" },
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
      { id: "workers", title: "Workers", unit: "count" },
      { id: "backpressure", title: "Backpressure", unit: "rate", desc: "Work dropped or refused because a queue was full." },
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

// ---- indexes and jobs

function progress(j: Job): number {
  if (!j.total) return 0;
  return Math.min(100, (j.scanned / j.total) * 100);
}

function IndexRow({ ix, onAction, busy }: { ix: IndexInfo; onAction: (fn: () => Promise<unknown>) => void; busy: boolean }) {
  const running = ix.running;
  const last = ix.last;
  return (
    <div className="index">
      <div className="index-head">
        <strong>{ix.name}</strong>
        {ix.available && <span className="muted">~{formatValue(ix.estimate, "count")} entries</span>}
      </div>
      {!ix.available ? (
        <p className="muted">{ix.unavailable}</p>
      ) : running ? (
        <>
          <div className="bar" role="progressbar" aria-valuenow={Math.round(progress(running))} aria-valuemin={0} aria-valuemax={100}>
            <span style={{ width: `${progress(running)}%` }} />
          </div>
          <p className="muted">
            {running.kind} · scanned {formatValue(running.scanned, "count")} · indexed {formatValue(running.indexed, "count")} · started {ago(running.started_at)}
          </p>
          <div className="actions">
            <button disabled={busy} onClick={() => onAction(() => api.cancel(running.id))}>
              Cancel
            </button>
          </div>
        </>
      ) : (
        <>
          <p className="muted">
            {last ? (
              <>
                last {last.kind} <span className={`state ${last.state}`}>{last.state}</span> {ago(last.finished_at)}
                {last.error ? ` — ${last.error}` : ""}
              </>
            ) : (
              "no jobs yet"
            )}
          </p>
          <div className="actions">
            <button disabled={busy} onClick={() => onAction(() => api.rebuild(ix.name, ix.name === "zaps", false))}>
              Rebuild
            </button>
            {ix.name === "search" && (
              <button disabled={busy} onClick={() => onAction(() => api.rebuild(ix.name, true, false))} title="Build a new index and swap it in">
                Rebuild fresh
              </button>
            )}
            <button
              className="danger"
              disabled={busy}
              onClick={() => {
                if (confirm(`Empty the ${ix.name} index? Its answers stay empty until a rebuild.`)) onAction(() => api.drop(ix.name));
              }}
            >
              Drop
            </button>
          </div>
        </>
      )}
    </div>
  );
}

interface IndexesProps {
  onRunning: (n: number) => void;
  paused: boolean;
  viewing: boolean;
  onView: (id: string | null) => void;
}

function Indexes({ onRunning, paused, viewing, onView }: IndexesProps) {
  const [indexes, setIndexes] = useState<IndexInfo[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const refresh = useCallback(async () => {
    try {
      const list = await api.indexes();
      setIndexes(list);
      onRunning(list.filter((i) => i.running).length);
      setError("");
    } catch (e) {
      setError((e as Error).message);
    }
  }, [onRunning]);

  useEffect(() => {
    void refresh();
    if (paused) return;
    const timer = setInterval(() => !document.hidden && void refresh(), 3000);
    return () => clearInterval(timer);
  }, [refresh, paused]);

  const act = async (fn: () => Promise<unknown>) => {
    setBusy(true);
    try {
      await fn();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
      void refresh();
    }
  };

  return (
    <Panel id="indexes" title="Indexes" desc="Search and zap indexes; rebuilds run as jobs." className="w-24 wide" viewing={viewing} onView={onView}>
      {error && <p className="error">{error}</p>}
      <div className="indexes">
        {indexes.map((ix) => (
          <IndexRow key={ix.name} ix={ix} onAction={act} busy={busy} />
        ))}
      </div>
    </Panel>
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
  const [kiosk, setKiosk] = useState(url.kiosk);
  const [collapsed, setCollapsed] = useState<string[]>(() => url.collapsed ?? (load("collapsed") ?? "").split(",").filter(Boolean));
  const toggleRow = (id: string) => {
    const next = collapsed.includes(id) ? collapsed.filter((c) => c !== id) : [...collapsed, id];
    setCollapsed(next);
    save("collapsed", next.join(",") || null);
  };

  useEffect(() => {
    const q = serialize(location.search, { range, paused: until != null, theme, kiosk, view, collapsed });
    history.replaceState(history.state, "", location.pathname + q + location.hash);
  }, [range, until, theme, kiosk, view, collapsed]);

  useEffect(() => {
    // Esc closes a viewed panel first, then leaves kiosk mode.
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      if (view) setView(null);
      else setKiosk(false);
    };
    addEventListener("keydown", onKey);
    return () => removeEventListener("keydown", onKey);
  }, [view]);
  const latest = useLatest(store);

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
    <div className={kiosk ? "page kiosk" : "page"}>
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
          <button className="ghost" title="Kiosk mode: hide the toolbars (Esc exits)" onClick={() => setKiosk(true)}>
            Kiosk
          </button>
          <span className="muted mono" title={session.pubkey}>
            {session.pubkey.slice(0, 8)}…{session.pubkey.slice(-6)}
          </span>
          {session.expires_at > 0 && (
            <button onClick={onLogout} className="ghost">
              Sign out
            </button>
          )}
        </div>
      </header>

      <Kpis p={latest} jobs={jobs} store={store} {...tv} />

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

      <Indexes onRunning={setJobs} paused={until != null} viewing={view === "indexes"} onView={setView} />
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
