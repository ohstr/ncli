import { describe, expect, it } from "vitest";
import { parse, serialize, type UrlState } from "./urlstate";

describe("url state", () => {
  it("round-trips", () => {
    const s: UrlState = { range: 900, paused: true, theme: "dark", kiosk: true, view: "eose", collapsed: ["latency", "process"] };
    const q = serialize("", s);
    expect(q).toBe("?from=now-15m&refresh=paused&theme=dark&kiosk&view=eose&collapsed=latency,process");
    expect(parse(q)).toEqual(s);
  });

  it("leaves defaults out and keeps other parameters", () => {
    const s = parse("?t=abc");
    expect(s).toEqual({ range: 3600, paused: false, theme: undefined, kiosk: false, view: null, collapsed: undefined });
    expect(serialize("?t=abc&from=now-5m", s)).toBe("?t=abc");
    expect(serialize("", s)).toBe("");
  });

  it("clamps ranges to the 1h ring and ignores junk", () => {
    expect(parse("?from=now-6h").range).toBe(3600);
    expect(parse("?from=now-30m").range).toBe(1800);
    expect(parse("?from=yesterday&theme=pink").theme).toBeUndefined();
    expect(parse("?from=yesterday").range).toBe(3600);
  });
});
