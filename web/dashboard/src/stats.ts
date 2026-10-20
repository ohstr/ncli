// Pure helpers for stat panels and legends.

export type Level = "ok" | "warn" | "bad" | "";

// Thresholds are [warn, bad]: a value at or above each turns that color.
export type Thresholds = [number, number];

export function level(v: number, [warn, bad]: Thresholds): Level {
  if (!Number.isFinite(v)) return "";
  return v >= bad ? "bad" : v >= warn ? "warn" : "ok";
}

export interface Summary {
  last: number | null;
  max: number | null;
  mean: number | null;
}

// summarize returns a column's last, max and mean, skipping gaps.
export function summarize(col: ArrayLike<number | null | undefined>): Summary {
  let last: number | null = null;
  let max = -Infinity;
  let sum = 0;
  let n = 0;
  for (let i = 0; i < col.length; i++) {
    const v = col[i];
    if (v == null || !Number.isFinite(v)) continue;
    last = v;
    if (v > max) max = v;
    sum += v;
    n++;
  }
  return n ? { last, max, mean: sum / n } : { last: null, max: null, mean: null };
}

// sparkPath draws a column as an SVG path in a w×h box, at most ~w points,
// breaking the line at gaps.
export function sparkPath(col: (number | null)[], w: number, h: number): string {
  const step = Math.max(1, Math.ceil(col.length / w));
  const pts: (number | null)[] = [];
  for (let i = 0; i < col.length; i += step) pts.push(col[i]);
  const { max } = summarize(pts);
  if (max == null || pts.length < 2) return "";
  const top = max > 0 ? max : 1;
  let d = "";
  let pen = false;
  pts.forEach((v, i) => {
    if (v == null) {
      pen = false;
      return;
    }
    const x = (i / (pts.length - 1)) * w;
    const y = h - (Math.max(0, v) / top) * (h - 2) - 1;
    d += `${pen ? "L" : "M"}${x.toFixed(1)} ${y.toFixed(1)}`;
    pen = true;
  });
  return d;
}
