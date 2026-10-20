// Time windows over the store's 1h ring.

export const RANGES: [string, number][] = [
  ["5m", 300],
  ["15m", 900],
  ["30m", 1800],
  ["1h", 3600],
];

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

// windowRange returns [start, end) of the points in the last `range` seconds,
// ending at `until` (a paused view) or at the newest point.
export function windowRange(times: number[], range: number, until?: number): [number, number] {
  const end = until == null ? times.length : lowerBound(times, until + 1e-9);
  if (end === 0) return [0, 0];
  return [lowerBound(times, times[end - 1] - range), end];
}
