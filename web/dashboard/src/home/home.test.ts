import { describe, expect, it } from "vitest";
import { npubEncode as reference } from "nostr-tools/nip19";
import { npubEncode } from "./bech32";
import { nipLink } from "./nips";
import { contactLink, relayURL, safeURL } from "./relay";

describe("homepage helpers", () => {
  it("encodes npubs exactly like nostr-tools", () => {
    for (const hex of ["3c1db3dd55e2ff09ba5317dd8eec2339797e9e2ddf74591172735c47f3a2ad6e", "0".repeat(64), "f".repeat(64)]) {
      expect(npubEncode(hex)).toBe(reference(hex));
    }
    expect(npubEncode("not hex")).toBe("");
  });

  it("only lets http(s) URLs through from NIP-11 and profiles", () => {
    expect(safeURL("https://x.example/a.png")).toBe("https://x.example/a.png");
    expect(safeURL("javascript:alert(1)")).toBe("");
    expect(safeURL("data:text/html,<b>")).toBe("");
    expect(safeURL(42)).toBe("");
  });

  it("links contacts it recognizes, and nothing unsafe", () => {
    expect(contactLink("ops@x.example")).toEqual({ href: "mailto:ops@x.example", label: "ops@x.example" });
    expect(contactLink("https://x.example/contact")?.href).toBe("https://x.example/contact");
    expect(contactLink("javascript:alert(1)")).toEqual({ href: "", label: "javascript:alert(1)" });
    expect(contactLink("  ")).toBeNull();
  });

  it("derives the relay address from the page's own", () => {
    expect(relayURL({ protocol: "https:", host: "relay.example" })).toBe("wss://relay.example");
    expect(relayURL({ protocol: "http:", host: "127.0.0.1:5500" })).toBe("ws://127.0.0.1:5500");
  });

  it("links NIP specs with two-digit numbers", () => {
    expect(nipLink("1")).toMatch(/\/01\.md$/);
    expect(nipLink("57")).toMatch(/\/57\.md$/);
    expect(nipLink("CW")).toMatch(/\/CW\.md$/);
  });
});
