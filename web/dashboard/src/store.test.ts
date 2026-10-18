import { beforeEach, describe, expect, it, vi } from "vitest";
import { MetricsStore } from "./store";

// Frames run when the test says so.
let frames: FrameRequestCallback[] = [];
beforeEach(() => {
  frames = [];
  vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => {
    frames.push(cb);
    return frames.length;
  });
});
const flush = () => frames.splice(0).forEach((f) => f(0));

const point = (t: number, v: Record<string, number>) => ({ t: t * 1000, v, totals: {} });

describe("MetricsStore", () => {
  it("backfills, appends and trims to an hour", () => {
    const s = new MetricsStore();
    s.load({ interval_ms: 1_200_000, t: [1000, 2000].map((x) => x * 1000), series: { a: [1, 2] } });
    expect(s.times).toEqual([1000, 2000]);
    s.push(point(3000, { a: 3 }));
    s.push(point(4000, { a: 4 }));
    // interval 20m -> 3 points per hour
    expect(s.times).toEqual([2000, 3000, 4000]);
    expect(s.get("a")).toEqual([2, 3, 4]);
  });

  it("ignores a point it already has", () => {
    const s = new MetricsStore();
    s.load({ interval_ms: 2000, t: [5000], series: { a: [1] } });
    s.push(point(5, { a: 9 }));
    expect(s.get("a")).toEqual([1]);
  });

  it("starts a new series with nulls before its first value", () => {
    const s = new MetricsStore();
    s.load({ interval_ms: 2000, t: [1000, 2000], series: { a: [1, 2] } });
    s.push(point(3, { a: 3, b: 7 }));
    expect(s.get("b")).toEqual([null, null, 7]);
    expect(s.get("a")).toEqual([1, 2, 3]);
    expect(s.get("missing")).toEqual([null, null, null]);
  });

  it("lists keys by prefix without quantile siblings", () => {
    const s = new MetricsStore();
    s.load({ interval_ms: 2000, t: [1], series: { "x:a": [1], "x:b": [1], "x.p99": [1], "y:a": [1] } });
    expect(s.keys("x")).toEqual(["x:a", "x:b"]);
  });

  it("notifies once per frame however many points arrive", () => {
    const s = new MetricsStore();
    const fn = vi.fn();
    s.subscribe(fn);
    s.push(point(1, { a: 1 }));
    s.push(point(2, { a: 2 }));
    expect(fn).not.toHaveBeenCalled();
    flush();
    expect(fn).toHaveBeenCalledTimes(1);
  });
});
