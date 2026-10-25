import { useCallback, useEffect, useRef, useState } from "react";
import { api, ApiError, type IndexInfo, type Job, type Plan } from "./api";
import { actions, ago, cancellable, count, ended, kindLabel, lastTaskText, meta, note, type Pace, percent, planText, progressText, stateLabel, tone, track } from "./indexes";
import { Panel } from "./Panel";

const now = () => Date.now() / 1000;

// A card's own feedback: what its last action did, kept until the next one.
interface Feedback {
  tone: "ok" | "bad";
  text: string;
}

// A confirmation waiting on the admin: the plan, and for a drop the name
// to type.
interface Pending {
  index: string;
  kind: "rebuild" | "drop";
  plan: Plan;
}

function JobProgress({ job, t }: { job: Job; t: number }) {
  const pct = percent(job);
  // The recent pace, for an estimate that follows a job that slows down.
  const pace = useRef<{ id?: string; samples: Pace }>({ samples: [] });
  if (pace.current.id !== job.id) pace.current = { id: job.id, samples: [] };
  pace.current.samples = track(pace.current.samples, t, job.done);
  return (
    <>
      <div
        className={`bar${pct == null ? " indeterminate" : ""}`}
        role="progressbar"
        aria-label={`${job.index} ${kindLabel(job.kind)}`}
        aria-valuenow={pct == null ? undefined : Math.round(pct)}
        aria-valuemin={0}
        aria-valuemax={100}
      >
        <span style={{ width: `${pct ?? 100}%` }} />
      </div>
      <p className="muted">
        <strong>{kindLabel(job.kind)}</strong> {progressText(job, t, pace.current.samples)} · {job.trigger === "admin" ? "started by an admin" : "started by the relay"} {ago(job.started_at, t)}
        {job.reason ? ` — ${job.reason}` : ""}
      </p>
    </>
  );
}

function History({ index }: { index: string }) {
  const [jobs, setJobs] = useState<Job[] | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    api
      .jobs(index)
      .then(setJobs)
      .catch((e: Error) => setError(e.message));
  }, [index]);
  if (error) return <p className="error">{error}</p>;
  if (!jobs) return <p className="muted">loading…</p>;
  if (!jobs.length) return <p className="muted">No jobs yet.</p>;
  const t = now();
  return (
    <ul className="history">
      {jobs.slice(0, 10).map((j) => (
        <li key={j.id}>
          <span className={`state ${j.state}`}>{j.state}</span> {j.state === "running" ? progressText(j, t) : j.summary}{" "}
          <span className="muted">· {j.trigger} · {ago(j.started_at, t)}</span>
        </li>
      ))}
    </ul>
  );
}

interface CardProps {
  ix: IndexInfo;
  t: number;
  busy: boolean;
  feedback?: Feedback;
  onStart: (index: string, kind: "rebuild" | "drop") => void;
  onCancel: (job: Job) => void;
}

function IndexCard({ ix, t, busy, feedback, onStart, onCancel }: CardProps) {
  const [history, setHistory] = useState(false);
  const can = actions(ix);
  const running = ix.running;
  const last = ix.last;
  const line = note(ix, t);
  return (
    <div className="index" data-index={ix.name}>
      <div className="index-head">
        <strong>{ix.name}</strong>
        <span className={`badge ${tone(ix.state)}`} data-state={ix.state}>
          {stateLabel(ix.state)}
        </span>
      </div>
      {meta(ix, t) && <p className="muted">{meta(ix, t)}</p>}
      {lastTaskText(ix) && <p className="muted last-task">{lastTaskText(ix)}</p>}
      {ix.warning && (
        <p className="warn" role="status">
          {ix.warning}
        </p>
      )}
      {running ? <JobProgress job={running} t={t} /> : line && <p className={ix.state === "unreachable" ? "error" : "muted"}>{line}</p>}
      {!running && last && (
        <p className="muted last">
          <span className={`state ${last.state}`}>{last.state}</span> {last.summary} · {ago(last.finished_at, t)}
        </p>
      )}
      {feedback && (
        <p className={feedback.tone === "bad" ? "error feedback" : "feedback"} role="status">
          {feedback.text}
        </p>
      )}
      <div className="actions">
        {running?.id && cancellable(running) && (
          <button className="ghost" disabled={busy || running.phase === "cancelling"} onClick={() => onCancel(running)}>
            {running.phase === "cancelling" ? "Cancelling…" : "Cancel"}
          </button>
        )}
        {can.rebuild && (
          <button disabled={busy} onClick={() => onStart(ix.name, "rebuild")}>
            {busy ? "Working…" : "Rebuild"}
          </button>
        )}
        {can.drop && (
          <button className="danger" disabled={busy} onClick={() => onStart(ix.name, "drop")}>
            Drop
          </button>
        )}
        <button className="ghost" aria-expanded={history} onClick={() => setHistory(!history)}>
          {history ? "Hide history" : "History"}
        </button>
      </div>
      {history && <History index={ix.name} />}
    </div>
  );
}

function Confirm({ pending, onConfirm, onClose }: { pending: Pending; onConfirm: () => void; onClose: () => void }) {
  const [typed, setTyped] = useState("");
  const drop = pending.kind === "drop";
  const ok = !drop || typed === pending.index;
  return (
    <div className="overlay" onClick={onClose}>
      <div className="dialog" role="dialog" aria-modal="true" aria-labelledby="confirm-title" onClick={(e) => e.stopPropagation()}>
        <h3 id="confirm-title">{drop ? `Drop the ${pending.index} index?` : `Rebuild the ${pending.index} index?`}</h3>
        <p>{planText(pending.plan)}</p>
        {drop && (
          <label>
            Type <code>{pending.index}</code> to confirm
            <input autoFocus value={typed} onChange={(e) => setTyped(e.target.value)} aria-label="index name" />
          </label>
        )}
        <div className="actions">
          <button className="ghost" onClick={onClose}>
            Keep it
          </button>
          <button className={drop ? "danger" : ""} disabled={!ok} onClick={onConfirm} autoFocus={!drop}>
            {drop ? `Drop ${count(pending.plan.entries)} entries` : "Rebuild"}
          </button>
        </div>
      </div>
    </div>
  );
}

interface IndexesProps {
  onRunning: (n: number) => void;
  viewing: boolean;
  onView: (id: string | null) => void;
}

// Indexes shows each index's state and jobs, live, and lets an admin
// rebuild one or drop what a disabled one holds.
export function Indexes({ onRunning, viewing, onView }: IndexesProps) {
  const [indexes, setIndexes] = useState<IndexInfo[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState<string | null>(null);
  const [feedback, setFeedback] = useState<Record<string, Feedback>>({});
  const [pending, setPending] = useState<Pending | null>(null);
  const [toasts, setToasts] = useState<Job[]>([]);
  const [t, setT] = useState(now);
  const prev = useRef<IndexInfo[]>([]);

  const refresh = useCallback(async () => {
    try {
      const list = await api.indexes();
      const done = ended(prev.current, list);
      if (done.length) {
        setToasts((ts) => [...ts, ...done]);
        setFeedback((f) => {
          const next = { ...f };
          for (const j of done) next[j.index] = { tone: j.state === "succeeded" ? "ok" : "bad", text: j.summary };
          return next;
        });
      }
      prev.current = list;
      setIndexes(list);
      setT(now());
      onRunning(list.filter((i) => i.running).length);
      setError("");
    } catch (e) {
      setError((e as Error).message);
    }
  }, [onRunning]);

  // Faster while a job runs; index work is shown live even when the charts
  // are paused.
  const active = indexes.some((i) => i.running) || busy != null;
  useEffect(() => {
    void refresh();
    const timer = setInterval(() => !document.hidden && void refresh(), active ? 1000 : 3000);
    return () => clearInterval(timer);
  }, [refresh, active]);

  useEffect(() => {
    if (!toasts.length) return;
    const timer = setTimeout(() => setToasts((ts) => ts.slice(1)), 6000);
    return () => clearTimeout(timer);
  }, [toasts]);

  const fail = (index: string, e: unknown) => {
    const err = e as ApiError;
    let text = err.message;
    if (err.job?.id) text += ` (job ${err.job.id}, ${kindLabel(err.job.kind)})`;
    setFeedback((f) => ({ ...f, [index]: { tone: "bad", text } }));
  };

  const ask = async (index: string, kind: "rebuild" | "drop") => {
    setBusy(index);
    setFeedback((f) => {
      const next = { ...f };
      delete next[index];
      return next;
    });
    try {
      setPending({ index, kind, plan: await api.plan(index, kind) });
    } catch (e) {
      fail(index, e);
    } finally {
      setBusy(null);
    }
  };

  const start = async () => {
    if (!pending) return;
    const { index, kind } = pending;
    setPending(null);
    setBusy(index);
    try {
      const job = await api.start(index, kind);
      setFeedback((f) => ({ ...f, [index]: { tone: "ok", text: job.summary } }));
      // Show it at once; the next poll has the relay's view.
      if (!job.noop && job.id) {
        setIndexes((list) => list.map((ix) => (ix.name === index ? { ...ix, running: job } : ix)));
        prev.current = prev.current.map((ix) => (ix.name === index ? { ...ix, running: job } : ix));
      }
    } catch (e) {
      fail(index, e);
    } finally {
      setBusy(null);
      void refresh();
    }
  };

  const cancel = async (job: Job) => {
    if (!job.id) return;
    setBusy(job.index);
    try {
      const j = await api.cancel(job.id);
      setFeedback((f) => ({ ...f, [job.index]: { tone: "ok", text: j.noop ? j.summary : `Cancelling job ${j.id}…` } }));
    } catch (e) {
      fail(job.index, e);
    } finally {
      setBusy(null);
      void refresh();
    }
  };

  return (
    <Panel
      id="indexes"
      title="Indexes"
      desc="Search, zap and stats indexes. The relay keeps them current on its own — catching up, rebuilding, freeing old data — as jobs shown here."
      className="w-24 wide"
      viewing={viewing}
      onView={onView}
      menu={[{ label: "Refresh", run: () => void refresh() }]}
    >
      {error && <p className="error">{error}</p>}
      <div className="indexes">
        {indexes.map((ix) => (
          <IndexCard key={ix.name} ix={ix} t={t} busy={busy === ix.name} feedback={feedback[ix.name]} onStart={ask} onCancel={cancel} />
        ))}
      </div>
      {pending && <Confirm pending={pending} onConfirm={() => void start()} onClose={() => setPending(null)} />}
      <div className="toasts" aria-live="polite">
        {toasts.map((j) => (
          <div key={j.id} className={`toast ${j.state === "succeeded" ? "ok" : "bad"}`}>
            {j.summary}
          </div>
        ))}
      </div>
    </Panel>
  );
}
