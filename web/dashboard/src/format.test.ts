import { describe, expect, it } from "vitest";
import { ago, formatBytes, formatMs, formatUptime, formatValue, identityLabel } from "./format";

describe("format", () => {
  it("bytes", () => {
    expect(formatBytes(0)).toBe("0 B");
    expect(formatBytes(1536)).toBe("1.5 KiB");
    expect(formatBytes(9.2 * 2 ** 30)).toBe("9.2 GiB");
  });

  it("milliseconds", () => {
    expect(formatMs(0.4)).toBe("400 µs");
    expect(formatMs(2.5)).toBe("2.5 ms");
    expect(formatMs(25)).toBe("25 ms");
    expect(formatMs(1500)).toBe("1.5 s");
  });

  it("values by unit", () => {
    expect(formatValue(1200, "rate")).toBe("1.2k/s");
    expect(formatValue(1200, "rate", true)).toBe("1.2k");
    expect(formatValue(12.34, "percent")).toBe("12.3%");
    expect(formatValue(0.015, "cores")).toBe("0.015");
    expect(formatValue(2_500_000, "count")).toBe("2.5M");
  });

  it("uptime and relative time", () => {
    expect(formatUptime(65)).toBe("1m 5s");
    expect(formatUptime(3 * 3600 + 120)).toBe("3h 2m");
    expect(formatUptime(2 * 86400 + 3600)).toBe("2d 1h");
    expect(ago(undefined)).toBe("–");
    expect(ago(Date.now() / 1000 - 5)).toBe("just now");
    expect(ago(Date.now() / 1000 - 7200)).toBe("2h ago");
  });

  it("identity", () => {
    const pk = "3bf0c63fcb93463407af97a5e5ee64fa883d107ef9e558472c4eb9aaaefa459d";
    expect(identityLabel(pk)).toBe("npub180cvv…yjh6w6");
    expect(identityLabel(pk, "bob@example.com")).toBe("bob@example.com");
    expect(identityLabel(pk, "_@example.com")).toBe("example.com");
    expect(identityLabel(pk)).not.toContain(pk.slice(0, 8));
  });
});
