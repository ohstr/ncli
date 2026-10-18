import { useEffect, useRef } from "react";
import uPlot from "uplot";
import "uplot/dist/uPlot.min.css";
import type { MetricsStore } from "./store";
import { formatValue, type Unit } from "./format";

export interface Line {
  key: string;
  label: string;
  // scale multiplies the stored value (e.g. 1000 for seconds -> ms).
  scale?: number;
}

const PALETTE = ["--c1", "--c2", "--c3", "--c4", "--c5", "--c6", "--c7", "--c8"];

function clock(t: number): string {
  return new Date(t * 1000).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

function cssVar(name: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim() || "#888";
}

interface Props {
  store: MetricsStore;
  title: string;
  unit: Unit;
  lines: (store: MetricsStore) => Line[];
  stacked?: boolean;
  height?: number;
}

// Chart draws one panel with uPlot. It redraws from the store on each
// animation-frame notification and rebuilds only when its series change.
export function Chart({ store, title, unit, lines, height = 150 }: Props) {
  const host = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const el = host.current;
    if (!el) return;
    let plot: uPlot | null = null;
    let signature = "";
    let current: Line[] = [];

    const data = (): uPlot.AlignedData => [
      store.times,
      ...current.map((l) => {
        const col = store.get(l.key);
        return l.scale ? col.map((v) => (v == null ? null : v * l.scale!)) : col;
      }),
    ];

    const build = () => {
      plot?.destroy();
      const grid = { stroke: cssVar("--grid"), width: 1 };
      const axis = { stroke: cssVar("--muted"), grid, ticks: grid, font: "11px system-ui, sans-serif" };
      plot = new uPlot(
        {
          width: Math.max(el.clientWidth, 200),
          height,
          legend: { live: true },
          cursor: { points: { size: 5 }, drag: { x: false, y: false } },
          scales: { x: { time: true }, y: { range: (_u, _min, max) => [0, max > 0 ? max * 1.1 : 1] } },
          axes: [
            { ...axis, space: 70, values: (_u, ticks) => ticks.map(clock) },
            { ...axis, size: 64, values: (_u, ticks) => ticks.map((v) => formatValue(v, unit, true)) },
          ],
          series: [
            { label: "Time", value: (_u: uPlot, v: number | null) => (v == null ? "–" : clock(v)) },
            ...current.map((l, i) => ({
              label: l.label,
              stroke: cssVar(PALETTE[i % PALETTE.length]),
              width: 1.5,
              points: { show: false },
              spanGaps: false,
              value: (_u: uPlot, v: number | null) => (v == null ? "–" : formatValue(v, unit)),
            })),
          ],
        },
        data(),
        el,
      );
    };

    const update = () => {
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
    };

    update();
    const unsubscribe = store.subscribe(update);
    const ro = new ResizeObserver(() => plot?.setSize({ width: Math.max(el.clientWidth, 200), height }));
    ro.observe(el);
    const theme = matchMedia("(prefers-color-scheme: dark)");
    const onTheme = () => {
      signature = "";
      update();
    };
    theme.addEventListener("change", onTheme);
    return () => {
      unsubscribe();
      ro.disconnect();
      theme.removeEventListener("change", onTheme);
      plot?.destroy();
    };
  }, [store, lines, unit, height]);

  return (
    <section className="panel">
      <h3>{title}</h3>
      <div ref={host} className="chart" />
    </section>
  );
}
