import { api, type Columns, type Point } from "./api";

// MetricsStore holds the columnar history the charts read, appends live
// points from the server's stream, and notifies listeners at most once per
// animation frame. Charts redraw in place from it; React never re-renders
// for a new point.
export class MetricsStore {
  times: number[] = []; // unix seconds, uPlot's x unit
  series = new Map<string, (number | null)[]>();
  latest: Point | null = null;
  // window is how many seconds of history are kept, as the server keeps.
  window = 3600;
  private loadedAt = 0;
  private reloading = false;
  private listeners = new Set<() => void>();
  private frame = 0;
  private source: EventSource | null = null;
  connected = false;

  load(cols: Columns) {
    this.window = (cols.window_ms || 3_600_000) / 1000;
    this.times = cols.t.map((ms) => ms / 1000);
    this.series = new Map(Object.entries(cols.series));
    this.loadedAt = this.times.at(-1) ?? 0;
    this.notify();
  }

  push(p: Point) {
    this.latest = p;
    const n = this.times.length;
    const t = p.t / 1000;
    if (n > 0 && t <= this.times[n - 1]) return; // already have it
    let drop = 0;
    while (drop < n && this.times[drop] < t - this.window) drop++;
    this.times.splice(0, drop);
    this.times.push(t);
    for (const [key, col] of this.series) {
      col.splice(0, drop);
      col.push(key in p.v ? p.v[key] : null);
    }
    for (const key of Object.keys(p.v)) {
      if (!this.series.has(key)) {
        const col: (number | null)[] = new Array(this.times.length).fill(null);
        col[col.length - 1] = p.v[key];
        this.series.set(key, col);
      }
    }
    // Live points are full resolution; after an hour the server has rolled
    // them up, so fetching again keeps a day-long view small.
    if (this.source && t - this.loadedAt > 3600) void this.reload();
    this.notify();
  }

  private async reload() {
    if (this.reloading) return;
    this.reloading = true;
    try {
      this.load(await api.history());
    } catch {
      /* keep what we have; the next point tries again */
    } finally {
      this.reloading = false;
    }
  }

  get(key: string): (number | null)[] {
    return this.series.get(key) ?? new Array(this.times.length).fill(null);
  }

  // keys returns series keys starting with prefix, e.g. "events_rejected_total:".
  keys(prefix: string): string[] {
    return [...this.series.keys()].filter((k) => k.startsWith(prefix) && !k.includes(".")).sort();
  }

  subscribe(fn: () => void): () => void {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  }

  private notify() {
    if (this.frame) return;
    this.frame = requestAnimationFrame(() => {
      this.frame = 0;
      for (const fn of this.listeners) fn();
    });
  }

  // start backfills history and follows the stream; it stops while the tab
  // is hidden, so a background tab costs the relay nothing.
  async start(onState: (connected: boolean) => void) {
    const open = async () => {
      if (this.source) return;
      try {
        this.load(await api.history());
      } catch {
        /* the stream still fills in */
      }
      const es = new EventSource("/admin/api/stream", { withCredentials: true });
      es.addEventListener("point", (e) => this.push(JSON.parse((e as MessageEvent).data)));
      es.onopen = () => {
        this.connected = true;
        onState(true);
      };
      es.onerror = () => {
        this.connected = false;
        onState(false);
      };
      this.source = es;
    };
    const close = () => {
      this.source?.close();
      this.source = null;
    };
    document.addEventListener("visibilitychange", () => (document.hidden ? close() : void open()));
    if (!document.hidden) await open();
  }

  stop() {
    this.source?.close();
    this.source = null;
  }
}
