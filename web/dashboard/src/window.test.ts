import { describe, expect, it } from "vitest";
import { windowRange } from "./window";

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
