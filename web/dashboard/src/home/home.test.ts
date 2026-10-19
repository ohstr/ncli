import { describe, expect, it } from "vitest";
import { npubEncode as reference } from "nostr-tools/nip19";
import { npubEncode } from "./bech32";
import { nipLink } from "./nips";
import { ago, contactLink, iconCandidates, isTopLevelNote, noteParts, relayURL, safeURL } from "./relay";

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

  it("splits note content into text, links, images and mentions", () => {
    const npub = "npub1sg6plzptd64u62a878hep2kev88swjh3tw00gjsfl8f237lmu63q0uf63m";
    expect(noteParts(`hi https://x.example/a.png and https://x.example/page. nostr:${npub}!`)).toEqual([
      { type: "text", value: "hi " },
      { type: "image", value: "https://x.example/a.png" },
      { type: "text", value: " and " },
      { type: "link", value: "https://x.example/page" },
      { type: "text", value: ". " },
      { type: "mention", value: npub },
      { type: "text", value: "!" },
    ]);
    expect(noteParts("<script>alert(1)</script>")).toEqual([{ type: "text", value: "<script>alert(1)</script>" }]);
  });

  it("keeps top-level notes only", () => {
    const ev = { id: "a", pubkey: "p", kind: 1, created_at: 0, content: "" };
    expect(isTopLevelNote({ ...ev, tags: [] })).toBe(true);
    expect(isTopLevelNote({ ...ev, tags: [["e", "x"]] })).toBe(false);
    expect(isTopLevelNote({ ...ev, kind: 7, tags: [] })).toBe(false);
  });

  it("formats relative times", () => {
    expect(ago(1000, 1030)).toBe("now");
    expect(ago(1000, 1000 + 5 * 60)).toBe("5m");
    expect(ago(1000, 1000 + 3 * 3600)).toBe("3h");
    expect(ago(1000, 1000 + 2 * 86400)).toBe("2d");
  });

  it("orders icon sources NIP-11 first, dropping unusable ones", () => {
    expect(iconCandidates("https://r.example/i.png", "https://p.example/p.png")).toEqual(["https://r.example/i.png", "https://p.example/p.png"]);
    expect(iconCandidates("javascript:x", "https://p.example/p.png")).toEqual(["https://p.example/p.png"]);
    expect(iconCandidates(undefined, undefined)).toEqual([]);
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
