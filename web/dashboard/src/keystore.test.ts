import { beforeEach, describe, expect, it, vi } from "vitest";
import { getPublicKey } from "nostr-tools/pure";
import { canRemember, forgetKey, savedKey, saveKey, unlockKey } from "./keystore";

const hex = "0acd12cbf0fb87cd13b17bc9b57dffd11b3870b407984cec5a4ce2a69b90268c";
const sk = () => Uint8Array.from(hex.match(/../g)!.map((h) => parseInt(h, 16)));

// A Map-backed localStorage; logn 1 keeps scrypt fast in tests.
beforeEach(() => {
  const m = new Map<string, string>();
  vi.stubGlobal("window", {
    localStorage: {
      getItem: (k: string) => m.get(k) ?? null,
      setItem: (k: string, v: string) => void m.set(k, v),
      removeItem: (k: string) => void m.delete(k),
    },
  });
});

describe("keystore", () => {
  it("stores only an ncryptsec, never the key", () => {
    const saved = saveKey(sk(), "correct horse", 1);
    expect(saved.ncryptsec.startsWith("ncryptsec1")).toBe(true);
    expect(saved.pubkey).toBe(getPublicKey(sk()));
    const raw = JSON.stringify(savedKey());
    expect(raw).not.toContain(hex);
    expect(raw).not.toContain("nsec1");
  });

  it("unlocks with the password and refuses a wrong one", () => {
    saveKey(sk(), "correct horse", 1);
    expect(unlockKey("correct horse")).toEqual(sk());
    expect(() => unlockKey("wrong horse")).toThrow("Wrong password.");
  });

  it("rejects a short password", () => {
    expect(() => saveKey(sk(), "short", 1)).toThrow(/at least 8/);
    expect(savedKey()).toBeNull();
  });

  it("forgets", () => {
    saveKey(sk(), "correct horse", 1);
    forgetKey();
    expect(savedKey()).toBeNull();
    expect(() => unlockKey("correct horse")).toThrow(/No key/);
  });

  it("treats tampered storage as nothing saved", () => {
    window.localStorage.setItem("ncli-dashboard:saved-key", JSON.stringify({ ncryptsec: "nsec1plain", pubkey: "x" }));
    expect(savedKey()).toBeNull();
  });

  it("works without storage, just without remembering", () => {
    vi.stubGlobal("window", {
      get localStorage(): Storage {
        throw new Error("blocked");
      },
    });
    expect(canRemember()).toBe(false);
    expect(savedKey()).toBeNull();
  });
});
