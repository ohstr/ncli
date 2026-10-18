import { describe, expect, it, vi } from "vitest";
import { nsecEncode } from "nostr-tools/nip19";
import { getPublicKey, verifyEvent } from "nostr-tools/pure";
import { bunkerSigner, isRelayURL, nostrConnect, parseSecretKey, secretKeySigner } from "./signers";

const hex = "0acd12cbf0fb87cd13b17bc9b57dffd11b3870b407984cec5a4ce2a69b90268c";
const bytes = () => Uint8Array.from(hex.match(/../g)!.map((h) => parseInt(h, 16)));

describe("parseSecretKey", () => {
  it("accepts nsec and hex", () => {
    expect(parseSecretKey(nsecEncode(bytes()))).toEqual(bytes());
    expect(parseSecretKey(`  ${hex.toUpperCase()} `)).toEqual(bytes());
  });

  it("rejects anything else without echoing it", () => {
    for (const bad of ["", "npub1xyz", "nsec1notvalid", hex.slice(1), "zz".repeat(32)]) {
      expect(() => parseSecretKey(bad)).toThrow(/Not a secret key/);
    }
    try {
      parseSecretKey("nsec1secretlooking");
    } catch (e) {
      expect((e as Error).message).not.toContain("secretlooking");
    }
  });
});

describe("secretKeySigner", () => {
  it("signs a valid event as the key's pubkey, then wipes the key", async () => {
    const sk = bytes();
    const pub = getPublicKey(bytes());
    const ev = (await secretKeySigner(sk)({
      kind: 27235,
      created_at: 1,
      tags: [["u", "http://x/admin/api/login"]],
      content: "",
    })) as Parameters<typeof verifyEvent>[0];
    expect(verifyEvent(ev)).toBe(true);
    expect(ev.pubkey).toBe(pub);
    expect(sk.every((b) => b === 0)).toBe(true);
  });
});

describe("remote signers", () => {
  it("makes a nostrconnect URI for one relay, asking only to sign the login kind", () => {
    const { uri } = nostrConnect("wss://relay.example");
    const u = new URL(uri);
    expect(u.protocol).toBe("nostrconnect:");
    expect(u.searchParams.get("relay")).toBe("wss://relay.example");
    expect(u.searchParams.get("perms")).toBe("sign_event:27235");
    expect(u.searchParams.get("secret")).toMatch(/^[0-9a-f]{24}$/);
    expect(u.searchParams.get("name")).toBe("ncli relay dashboard");
    // A fresh client key and secret every time.
    expect(nostrConnect("wss://relay.example").uri).not.toBe(uri);
  });

  it("rejects a malformed bunker URL before connecting", async () => {
    vi.stubGlobal("fetch", async () => new Response("{}", { status: 404 }));
    await expect(bunkerSigner("not a bunker", () => {})).rejects.toThrow(/Not a bunker URL/);
    vi.unstubAllGlobals();
  });

  it("only allows secure relays, or plain ws on localhost", () => {
    expect(isRelayURL("wss://relay.nsec.app")).toBe(true);
    expect(isRelayURL("ws://localhost:7777")).toBe(true);
    expect(isRelayURL("ws://relay.example")).toBe(false);
    expect(isRelayURL("https://relay.example")).toBe(false);
    expect(isRelayURL("nope")).toBe(false);
  });
});
