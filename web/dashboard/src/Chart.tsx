import { useEffect, useRef, useState } from "react";
import uPlot from "uplot";
import "uplot/dist/uPlot.min.css";
import type { MetricsStore } from "./store";
import { formatValue, type Unit } from "./format";
import { download, Panel } from "./Panel";
import { summarize, type Thresholds } from "./stats";
import { onThemeChange } from "./theme";
import { windowRange } from "./window";

export interface Line {
  key: string;
  label: string;
  // scale multiplies the stored value (e.g. 1000 for seconds -> ms).
  scale?: number;
}

const PALETTE = ["--c1", "--c2", "--c3", "--c4", "--c5", "--c6", "--c7", "--c8"];

const UNITS: Record<Unit, string> = { rate: "/s", ms: "ms", count: "", bytes: "bytes", cores: "cores", percent: "%" };

// Every chart's crosshair follows the one under the pointer.
const SYNC = "relay";

function clock(t: number): string {
  return new Date(t * 1000).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

function cssVar(name: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim() || "#888";
}

function h(tag: string, cls = "", text = ""): HTMLElement {
  const e = document.createElement(tag);
  e.className = cls;
  e.textContent = text;
  return e;
}

interface Props {
  id: string;
  store: MetricsStore;
  title: string;
  unit: Unit;
  lines: (store: MetricsStore) => Line[];
  desc?: string;
  // w is the grid width in 24ths; h the plot height.
  w: number;
  h?: number;
  // thresholds draw dashed warn/bad lines.
  thresholds?: Thresholds;
  // range is the visible window in seconds; until freezes it (paused).
  range: number;
  until?: number;
  viewing?: boolean;
  onView?: (id: string | null) => void;
}

// Chart draws one panel with uPlot. It redraws from the store on each
// animation-frame notification and rebuilds only when its series change.
export function Chart({ id, store, title, unit, lines, desc, w, h: height = 200, thresholds, range, until, viewing, onView }: Props) {
  const host = useRef<HTMLDivElement>(null);
  const table = useRef<HTMLTableSectionElement>(null);
  const [legend, setLegend] = useState(true);
  const view = useRef({ range, until, viewing, redraw: () => {}, csv: (): string => "" });
  Object.assign(view.current, { range, until, viewing });

  useEffect(() => {
    const el = host.current;
    const body = table.current;
    if (!el || !body) return;
    let plot: uPlot | null = null;
    let signature = "";
    let current: Line[] = [];
    let cells: HTMLElement[][] = [];
    let colors: string[] = [];
    const hidden = new Set<string>();
    const fmt = (v: number | null | undefined) => (v == null ? "–" : formatValue(v, unit));
    const size = () => ({
      width: Math.max(el.clientWidth, 200),
      height: view.current.viewing ? Math.max(200, innerHeight - 160 - body.clientHeight) : height,
    });

    const data = (): uPlot.AlignedData => {
      const [a, b] = windowRange(store.times, view.current.range, view.current.until);
      return [
        store.times.slice(a, b),
        ...current.map((l) => {
          const col = store.get(l.key).slice(a, b);
          return l.scale ? col.map((v) => (v == null ? null : v * l.scale!)) : col;
        }),
      ];
    };

    // Legend: click isolates a series (again shows all), shift-click toggles.
    const pick = (i: number, shift: boolean) => {
      if (!plot) return;
      const keys = current.map((l) => l.key);
      const only = keys.every((k, j) => (j === i) !== hidden.has(k));
      if (shift) {
        if (hidden.has(keys[i])) hidden.delete(keys[i]);
        else hidden.add(keys[i]);
      } else {
        hidden.clear();
        if (!only) keys.forEach((k, j) => j !== i && hidden.add(k));
      }
      keys.forEach((k, j) => {
        plot!.setSeries(j + 1, { show: !hidden.has(k) });
        body.rows[j]?.classList.toggle("off", hidden.has(k));
      });
    };

    const fillLegend = () => {
      const d = plot?.data;
      cells.forEach((row, i) => {
        const s = summarize(d?.[i + 1] ?? []);
        row[0].textContent = fmt(s.last);
        row[1].textContent = fmt(s.max);
        row[2].textContent = fmt(s.mean);
      });
    };

    const buildLegend = () => {
      body.replaceChildren();
      cells = current.map((l, i) => {
        const tr = document.createElement("tr");
        const name = h("td", "name");
        const mark = h("i");
        mark.style.background = colors[i];
        name.append(mark, l.label);
        const row = [h("td"), h("td"), h("td")];
        tr.append(name, ...row);
        tr.classList.toggle("off", hidden.has(l.key));
        tr.onclick = (e) => pick(i, e.shiftKey);
        body.append(tr);
        return row;
      });
    };

    // Tooltip: every series at the cursor, largest first, on the hovered chart.
    const tooltip = (): uPlot.Plugin => {
      const tip = h("div", "tip");
      let over = false;
      return {
        hooks: {
          init: (u) => {
            u.over.append(tip);
            u.over.addEventListener("mouseenter", () => (over = true));
            u.over.addEventListener("mouseleave", () => {
              over = false;
              tip.hidden = true;
            });
            tip.hidden = true;
          },
          setCursor: (u) => {
            const i = u.cursor.idx;
            if (!over || i == null || u.cursor.left == null || u.cursor.left < 0) {
              tip.hidden = true;
              return;
            }
            const rows = u.series
              .slice(1)
              .map((s, j) => ({ s, v: u.data[j + 1][i], c: colors[j] }))
              .filter((r) => r.s.show && r.v != null)
              .sort((a, b) => b.v! - a.v!);
            if (!rows.length) {
              tip.hidden = true;
              return;
            }
            tip.replaceChildren(h("div", "tip-time", clock(u.data[0][i])));
            for (const r of rows) {
              const line = h("div");
              const mark = h("i");
              mark.style.background = r.c;
              line.append(mark, h("span", "", String(r.s.label)), h("b", "", fmt(r.v)));
              tip.append(line);
            }
            tip.hidden = false;
            const left = u.cursor.left;
            const flip = left > u.over.clientWidth / 2;
            tip.style.left = flip ? "" : `${left + 12}px`;
            tip.style.right = flip ? `${u.over.clientWidth - left + 12}px` : "";
            tip.style.top = `${Math.max(0, (u.cursor.top ?? 0) - 10)}px`;
          },
        },
      };
    };

    // Threshold lines, dashed, where they fall inside the y range.
    const thresholdLines = (): uPlot.Plugin => ({
      hooks: {
        draw: (u) => {
          if (!thresholds) return;
          const ctx = u.ctx;
          const colors = [cssVar("--warn"), cssVar("--bad")];
          ctx.save();
          ctx.setLineDash([6, 4]);
          ctx.lineWidth = devicePixelRatio;
          thresholds.forEach((t, i) => {
            if (t > (u.scales.y.max ?? 0)) return;
            const y = Math.round(u.valToPos(t, "y", true));
            ctx.strokeStyle = colors[i];
            ctx.beginPath();
            ctx.moveTo(u.bbox.left, y);
            ctx.lineTo(u.bbox.left + u.bbox.width, y);
            ctx.stroke();
          });
          ctx.restore();
        },
      },
    });

    const build = () => {
      plot?.destroy();
      const grid = { stroke: cssVar("--grid"), width: 1 };
      const axis = { stroke: cssVar("--muted"), grid, ticks: grid, font: "11px system-ui, sans-serif" };
      colors = current.map((_, i) => cssVar(PALETTE[i % PALETTE.length]));
      const single = current.length === 1;
      plot = new uPlot(
        {
          ...size(),
          legend: { show: false },
          plugins: [tooltip(), thresholdLines()],
          cursor: { sync: { key: SYNC }, y: false, points: { size: 5 }, drag: { x: false, y: false } },
          scales: {
            x: { time: true, range: (_u, min, max) => (max == null ? [min, max] : [max - view.current.range, max]) },
            y: { range: (_u, _min, max) => [0, max > 0 ? max * 1.1 : 1] },
          },
          axes: [
            { ...axis, space: 70, values: (_u, ticks) => ticks.map(clock) },
            { ...axis, size: 64, values: (_u, ticks) => ticks.map((v) => formatValue(v, unit, true)) },
          ],
          series: [
            {},
            ...current.map((l, i) => ({
              label: l.label,
              show: !hidden.has(l.key),
              stroke: colors[i],
              width: 1.5,
              points: { show: false },
              spanGaps: false,
              // A lone series gets Grafana's soft gradient fill.
              fill: single
                ? (u: uPlot) => {
                    const g = u.ctx.createLinearGradient(0, u.bbox.top, 0, u.bbox.top + u.bbox.height);
                    g.addColorStop(0, colors[i] + "40");
                    g.addColorStop(1, colors[i] + "00");
                    return g;
                  }
                : undefined,
            })),
          ],
        },
        data(),
        el,
      );
      buildLegend();
      fillLegend();
    };

    const update = (force = false) => {
      if (plot && !force && view.current.until != null) return;
      const next = lines(store);
      const sig = next.map((l) => l.key).join("|");
      if (sig !== signature || !plot) {
        signature = sig;
        current = next;
        el.classList.toggle("empty", next.length === 0);
        build();
        return;
      }
      plot.setData(data(), true);
      fillLegend();
    };

    update();
    view.current.redraw = () => {
      update(true);
      plot?.setSize(size());
    };
    view.current.csv = () => {
      const d = plot?.data ?? [[]];
      const rows = [["time", ...current.map((l) => l.label)].join(",")];
      d[0].forEach((t, i) => rows.push([new Date(t * 1000).toISOString(), ...d.slice(1).map((c) => c[i] ?? "")].join(",")));
      return rows.join("\n") + "\n";
    };
    const unsubscribe = store.subscribe(() => update());
    const ro = new ResizeObserver(() => plot?.setSize(size()));
    ro.observe(el);
    const offTheme = onThemeChange(() => {
      signature = "";
      update(true);
    });
    return () => {
      unsubscribe();
      ro.disconnect();
      offTheme();
      plot?.destroy();
    };
  }, [store, lines, unit, height, thresholds]);

  useEffect(() => view.current.redraw(), [range, until, viewing, legend]);

  return (
    <Panel
      id={id}
      title={title}
      hint={UNITS[unit]}
      desc={desc}
      className={`w-${w}${legend ? "" : " no-legend"}`}
      viewing={viewing}
      onView={onView}
      menu={[
        { label: "Inspect: download CSV", run: () => download(`${id}.csv`, view.current.csv()) },
        { label: legend ? "Hide legend" : "Show legend", run: () => setLegend(!legend) },
      ]}
    >
      <div ref={host} className="chart" />
      <table className="legend">
        <thead>
          <tr>
            <th>Name</th>
            <th>Last</th>
            <th>Max</th>
            <th>Mean</th>
          </tr>
        </thead>
        <tbody ref={table} />
      </table>
    </Panel>
  );
}
