import { describe, expect, it } from "vitest";
import { parse, serialize, type UrlState } from "./urlstate";

describe("url state", () => {
  it("round-trips", () => {
    const s: UrlState = { range: 21600, paused: true, theme: "dark", view: "eose", collapsed: ["latency", "process"] };
    const q = serialize("", s);
    expect(q).toBe("?from=now-6h&refresh=paused&theme=dark&view=eose&collapsed=latency,process");
    expect(parse(q)).toEqual(s);
  });

  it("leaves defaults out and keeps other parameters", () => {
    const s = parse("?t=abc");
    expect(s).toEqual({ range: 3600, paused: false, theme: undefined, view: null, collapsed: undefined });
    expect(serialize("?t=abc&from=now-5m", s)).toBe("?t=abc");
    expect(serialize("", s)).toBe("");
  });

  it("drops the old kiosk flag", () => {
    expect(serialize("?kiosk&t=abc", parse("?kiosk&t=abc"))).toBe("?t=abc");
  });

  it("clamps ranges to a day and ignores junk", () => {
    expect(parse("?from=now-48h").range).toBe(86400);
    expect(parse("?from=now-6h").range).toBe(21600);
    expect(parse("?from=yesterday&theme=pink").theme).toBeUndefined();
    expect(parse("?from=yesterday").range).toBe(3600);
  });
});
