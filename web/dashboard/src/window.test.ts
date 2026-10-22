import { describe, expect, it } from "vitest";
import { historyStart, windowRange } from "./window";

describe("historyStart", () => {
  // Ten minutes of points.
  const times = Array.from({ length: 11 }, (_, i) => 1000 + i * 60);

  it("names the oldest point when the range reaches past it", () => {
    expect(historyStart(times, 3600)).toBe(1000);
    expect(historyStart(times, 86400)).toBe(1000);
  });

  it("is null when history covers the range", () => {
    expect(historyStart(times, 300)).toBeNull();
    expect(historyStart(times, 600)).toBeNull();
    expect(historyStart(times, 650)).toBeNull(); // within a minute of slack
  });

  it("follows a paused view and an empty store", () => {
    expect(historyStart(times, 300, 1300)).toBeNull();
    expect(historyStart(times, 600, 1300)).toBe(1000);
    expect(historyStart([], 300)).toBeNull();
  });
});

describe("windowRange", () => {
  const times = [0, 2, 4, 6, 8, 10];

  it("keeps the last range seconds", () => {
    expect(windowRange(times, 4)).toEqual([3, 6]);
    expect(windowRange(times, 3600)).toEqual([0, 6]);
  });

  it("ends at a paused time", () => {
    expect(windowRange(times, 4, 6)).toEqual([1, 4]);
    expect(windowRange(times, 4, 7)).toEqual([1, 4]);
  });

  it("is empty without points", () => {
    expect(windowRange([], 300)).toEqual([0, 0]);
    expect(windowRange(times, 300, -1)).toEqual([0, 0]);
  });
});
