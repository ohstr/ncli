import { lazy, Suspense, useCallback, useEffect, useMemo, useState, useSyncExternalStore } from "react";
import { api, canRenew, onSession, renew, setCsrf, setRenewer, type IndexInfo, type Job, type Point, type Session } from "./api";
import { Chart, type Line } from "./Chart";
import { ago, formatBytes, formatMs, formatUptime, formatValue } from "./format";
import { MetricsStore } from "./store";
import mark from "./brand/ncli-mark.svg";

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

// ---- live values

function useLatest(store: MetricsStore): Point | null {
  return useSyncExternalStore(
    (fn) => store.subscribe(fn),
    () => store.latest,
  );
}

type Level = "ok" | "warn" | "bad" | "";

function Kpi({ label, value, sub, level = "" }: { label: string; value: string; sub?: string; level?: Level }) {
  return (
    <div className={`kpi ${level}`}>
      <span className="kpi-label">{label}</span>
      <span className="kpi-value">{value}</span>
      {sub && <span className="kpi-sub">{sub}</span>}
    </div>
  );
}

function level(v: number, warn: number, bad: number): Level {
  if (!Number.isFinite(v)) return "";
  return v >= bad ? "bad" : v >= warn ? "warn" : "ok";
}

function Kpis({ p, jobs }: { p: Point | null; jobs: number }) {
  const v = p?.v ?? {};
  const t = p?.totals ?? {};
  const n = (k: string) => v[k] ?? NaN;
  const accepted = n("events_total:accepted") || 0;
  const rejected = n("events_total:rejected") || 0;
  const handled = accepted + rejected + (n("events_total:duplicate") || 0);
  const rejectPct = handled > 0 ? (rejected / handled) * 100 : 0;
  const okP99 = n("event_ok_seconds.p99") * 1000;
  const eoseP99 = n("req_eose_seconds.p99") * 1000;
  const queuePct = (n("store_task_queue_depth") / (n("store_task_queue_capacity") || 1)) * 100;
  const denied = Object.entries(t)
    .filter(([k]) => k.startsWith("admin_denied_total:"))
    .reduce((a, [, x]) => a + x, 0);
  const ms = (x: number) => (Number.isFinite(x) ? formatMs(x) : "–");
  return (
    <div className="kpis">
      <Kpi label="Uptime" value={p ? formatUptime(n("uptime_seconds")) : "–"} />
      <Kpi label="Sessions" value={formatValue(n("ws_sessions") || 0, "count")} sub={`${formatValue(n("subscriptions_active") || 0, "count")} subs`} />
      <Kpi label="Events accepted" value={formatValue(accepted, "rate")} sub={`${formatValue(n("messages_total:REQ") || 0, "rate")} REQ`} />
      <Kpi label="Rejected" value={formatValue(rejectPct, "percent")} level={handled > 0 ? level(rejectPct, 5, 25) : ""} />
      <Kpi label="EVENT→OK p99" value={ms(okP99)} level={level(okP99, 250, 1000)} />
      <Kpi label="REQ→EOSE p99" value={ms(eoseP99)} level={level(eoseP99, 500, 2000)} />
      <Kpi label="Write queue" value={Number.isFinite(queuePct) ? formatValue(queuePct, "percent") : "–"} level={level(queuePct, 50, 90)} />
      <Kpi label="Store" value={Number.isFinite(n("store_size_bytes")) ? formatBytes(n("store_size_bytes")) : "–"} />
      <Kpi label="CPU" value={Number.isFinite(n("cpu_seconds_total")) ? formatValue(n("cpu_seconds_total"), "cores") : "–"} sub={Number.isFinite(n("rss_bytes")) ? formatBytes(n("rss_bytes")) : ""} />
      <Kpi label="Admin denied" value={formatValue(denied, "count")} level={denied > 0 ? "warn" : "ok"} />
      <Kpi label="Jobs" value={String(jobs)} sub={jobs ? "running" : "idle"} />
    </div>
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

function Indexes({ onRunning }: { onRunning: (n: number) => void }) {
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
    const timer = setInterval(() => !document.hidden && void refresh(), 3000);
    return () => clearInterval(timer);
  }, [refresh]);

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
    <section className="panel wide">
      <h3>Indexes</h3>
      {error && <p className="error">{error}</p>}
      <div className="indexes">
        {indexes.map((ix) => (
          <IndexRow key={ix.name} ix={ix} onAction={act} busy={busy} />
        ))}
      </div>
    </section>
  );
}

// ---- page

function Dashboard({ session, onLogout }: { session: Session; onLogout: () => void }) {
  const store = useMemo(() => new MetricsStore(), []);
  const [live, setLive] = useState(false);
  const [jobs, setJobs] = useState(0);
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
    <div className="page">
      <header>
        <img className="brand" src={mark} alt="" width="26" height="26" />
        <h1>Relay Monitor</h1>
        <span className={`dot ${live ? "live" : ""}`} title={live ? "live" : "reconnecting"} />
        <span className="muted">{live ? "live" : "reconnecting…"}</span>
        <span className="spacer" />
        <span className="muted mono" title={session.pubkey}>
          {session.pubkey.slice(0, 8)}…{session.pubkey.slice(-6)}
        </span>
        {session.expires_at > 0 && (
          <button onClick={onLogout} className="ghost">
            Sign out
          </button>
        )}
      </header>

      <Kpis p={latest} jobs={jobs} />

      <h2>Traffic</h2>
      <div className="grid">
        <Chart store={store} title="Messages / s" unit="rate" lines={CHARTS.messages} />
        <Chart store={store} title="Events / s" unit="rate" lines={CHARTS.events} />
        <Chart store={store} title="Events by kind / s" unit="rate" lines={CHARTS.classes} />
        <Chart store={store} title="Rejections by reason / s" unit="rate" lines={CHARTS.rejected} />
      </div>

      <h2>Latency</h2>
      <div className="grid">
        <Chart store={store} title="EVENT → OK" unit="ms" lines={CHARTS.eventOK} />
        <Chart store={store} title="REQ → EOSE" unit="ms" lines={CHARTS.eose} />
        <Chart store={store} title="Store, COUNT and sends" unit="ms" lines={CHARTS.store} />
      </div>

      <h2>Connections and load</h2>
      <div className="grid">
        <Chart store={store} title="Sessions and subscriptions" unit="count" lines={CHARTS.connections} />
        <Chart store={store} title="Connects and send problems / s" unit="rate" lines={CHARTS.churn} />
        <Chart store={store} title="Store queues" unit="count" lines={CHARTS.storeQueues} />
        <Chart store={store} title="Workers" unit="count" lines={CHARTS.workers} />
        <Chart store={store} title="Backpressure / s" unit="rate" lines={CHARTS.backpressure} />
        <Chart store={store} title="HTTP bridges / s" unit="rate" lines={CHARTS.http} />
      </div>

      <h2>Access</h2>
      <div className="grid">
        <Chart store={store} title="AUTH and admin / s" unit="rate" lines={CHARTS.auth} />
        <Chart store={store} title="Refused reads / s" unit="rate" lines={CHARTS.refusedReads} />
      </div>

      <h2>Process</h2>
      <div className="grid">
        <Chart store={store} title="CPU" unit="cores" lines={CHARTS.cpu} />
        <Chart store={store} title="Memory" unit="bytes" lines={CHARTS.memory} />
        <Chart store={store} title="Goroutines and fds" unit="count" lines={CHARTS.goroutines} />
      </div>

      <Indexes onRunning={setJobs} />
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
