// A secret key remembered on this browser, only ever as a NIP-49
// ncryptsec (scrypt + XChaCha20-Poly1305 under the user's password). The
// plaintext key is never written anywhere.
import { decrypt, encrypt } from "nostr-tools/nip49";
import { getPublicKey } from "nostr-tools/pure";

const STORAGE_KEY = "ncli-dashboard:saved-key";

// NIP-49's recommended cost: 2^16 scrypt rounds, about a second to unlock.
export const DEFAULT_LOGN = 16;
export const MIN_PASSWORD = 8;

export interface SavedKey {
  ncryptsec: string;
  pubkey: string;
  savedAt: number;
}

function storage(): Storage | null {
  try {
    return window.localStorage;
  } catch {
    return null; // blocked storage: remembering is simply unavailable
  }
}

export function savedKey(): SavedKey | null {
  try {
    const raw = storage()?.getItem(STORAGE_KEY);
    if (!raw) return null;
    const v = JSON.parse(raw) as SavedKey;
    if (typeof v.ncryptsec !== "string" || !v.ncryptsec.startsWith("ncryptsec1") || !/^[0-9a-f]{64}$/.test(v.pubkey)) return null;
    return v;
  } catch {
    return null;
  }
}

export function canRemember(): boolean {
  return storage() !== null;
}

// saveKey encrypts sk under password and remembers it.
export function saveKey(sk: Uint8Array, password: string, logn = DEFAULT_LOGN): SavedKey {
  if (password.length < MIN_PASSWORD) throw new Error(`Use a password of at least ${MIN_PASSWORD} characters.`);
  const saved: SavedKey = {
    // 0x02: this client doesn't track how the key was handled before.
    ncryptsec: encrypt(sk, password, logn, 0x02),
    pubkey: getPublicKey(sk),
    savedAt: Math.floor(Date.now() / 1000),
  };
  const s = storage();
  if (!s) throw new Error("This browser blocks storage, so the key can't be remembered.");
  s.setItem(STORAGE_KEY, JSON.stringify(saved));
  return saved;
}

// unlockKey decrypts the remembered key with password.
export function unlockKey(password: string): Uint8Array {
  const saved = savedKey();
  if (!saved) throw new Error("No key is remembered on this browser.");
  let sk: Uint8Array;
  try {
    sk = decrypt(saved.ncryptsec, password);
  } catch {
    throw new Error("Wrong password.");
  }
  if (getPublicKey(sk) !== saved.pubkey) {
    sk.fill(0);
    throw new Error("The remembered key is damaged; forget it and sign in again.");
  }
  return sk;
}

export function forgetKey(): void {
  storage()?.removeItem(STORAGE_KEY);
}
