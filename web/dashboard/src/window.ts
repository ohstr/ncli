// Time windows over the store's history: 2s points for the last hour, 1m
// rollups behind them for a day.

export const RANGES: [string, number][] = [
  ["5m", 300],
  ["15m", 900],
  ["30m", 1800],
  ["1h", 3600],
  ["3h", 10800],
  ["6h", 21600],
  ["12h", 43200],
  ["24h", 86400],
];

export const MAX_RANGE = 86400;

// lowerBound returns the first index whose time is >= t.
function lowerBound(times: number[], t: number): number {
  let lo = 0;
  let hi = times.length;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if (times[mid] < t) lo = mid + 1;
    else hi = mid;
  }
  return lo;
}

// historyStart is the oldest point's time when the window reaches further
// back than the history does (a relay up less than the range), else null.
// A minute of slack keeps a full day from flickering as rollups age out.
export function historyStart(times: number[], range: number, until?: number): number | null {
  const end = until == null ? times.length : lowerBound(times, until + 1e-9);
  if (end === 0) return null;
  return times[end - 1] - range < times[0] - 60 ? times[0] : null;
}

// windowRange returns [start, end) of the points in the last `range` seconds,
// ending at `until` (a paused view) or at the newest point.
export function windowRange(times: number[], range: number, until?: number): [number, number] {
  const end = until == null ? times.length : lowerBound(times, until + 1e-9);
  if (end === 0) return [0, 0];
  return [lowerBound(times, times[end - 1] - range), end];
}
