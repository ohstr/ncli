import { describe, expect, it } from "vitest";
import { level, sparkPath, summarize } from "./stats";

describe("level", () => {
  it("colors by threshold", () => {
    expect(level(10, [250, 1000])).toBe("ok");
    expect(level(250, [250, 1000])).toBe("warn");
    expect(level(1500, [250, 1000])).toBe("bad");
    expect(level(NaN, [250, 1000])).toBe("");
  });
});

describe("summarize", () => {
  it("ignores gaps", () => {
    expect(summarize([1, null, 5, 3, null])).toEqual({ last: 3, max: 5, mean: 3 });
  });

  it("is empty without values", () => {
    expect(summarize([null, null])).toEqual({ last: null, max: null, mean: null });
    expect(summarize([])).toEqual({ last: null, max: null, mean: null });
  });
});

describe("sparkPath", () => {
  it("breaks at gaps and fits the box", () => {
    expect(sparkPath([0, 2, null, 2], 30, 10)).toBe("M0.0 9.0L10.0 1.0M30.0 1.0");
  });

  it("needs two points", () => {
    expect(sparkPath([1], 30, 10)).toBe("");
  });
});
