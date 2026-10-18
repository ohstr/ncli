// Ways to sign the dashboard's login event besides a NIP-07 extension: a
// secret key held only for the one signature, or a NIP-46 remote signer
// (bunker:// URL, or a nostrconnect:// code a signer app scans).
import { decode } from "nostr-tools/nip19";
import { BunkerSigner, createNostrConnectURI, parseBunkerInput } from "nostr-tools/nip46";
import { SimplePool } from "nostr-tools/pool";
import { finalizeEvent, generateSecretKey, getPublicKey } from "nostr-tools/pure";
import type { LoginTemplate, SignFn } from "./api";

// parseSecretKey accepts an nsec or 64 hex characters.
export function parseSecretKey(input: string): Uint8Array {
  const s = input.trim();
  if (/^[0-9a-fA-F]{64}$/.test(s)) {
    return Uint8Array.from(s.match(/../g)!.map((h) => parseInt(h, 16)));
  }
  if (s.startsWith("nsec1")) {
    try {
      const d = decode(s);
      if (d.type === "nsec") return d.data;
    } catch {
      /* fall through */
    }
  }
  throw new Error("Not a secret key: paste an nsec1… or 64 hex characters.");
}

// secretKeySigner signs with sk, then zeroes it: the key is good for one
// signature and never leaves this tab.
export function secretKeySigner(sk: Uint8Array): SignFn {
  return async (ev: LoginTemplate) => {
    try {
      return finalizeEvent(ev, sk);
    } finally {
      sk.fill(0);
    }
  };
}

export const DEFAULT_SIGNER_RELAY = "wss://relay.nsec.app";

interface Remote {
  sign: SignFn;
  close: () => Promise<void>;
}

function remote(signer: BunkerSigner, pool: SimplePool): Remote {
  return {
    sign: (ev) => signer.signEvent(ev),
    // The signer unsubscribes over its sockets, so they close after it.
    close: async () => {
      await signer.close().catch(() => {});
      pool.destroy();
    },
  };
}

// bunkerSigner connects to the remote signer a bunker:// URL (or NIP-05
// name) points at. onAuth gets a URL the signer wants the user to open.
export async function bunkerSigner(input: string, onAuth: (url: string) => void): Promise<Remote> {
  const bp = await parseBunkerInput(input.trim());
  if (!bp) throw new Error("Not a bunker URL: expected bunker://<pubkey>?relay=wss://…");
  const pool = new SimplePool();
  const signer = BunkerSigner.fromBunker(generateSecretKey(), bp, { pool, onauth: onAuth });
  try {
    await signer.connect({ name: "ncli relay dashboard" });
  } catch (e) {
    pool.destroy();
    throw e;
  }
  return remote(signer, pool);
}

export interface NostrConnect {
  uri: string;
  // wait resolves once a signer app has scanned the code and connected.
  wait: (onAuth: (url: string) => void, signal: AbortSignal) => Promise<Remote>;
}

// nostrConnect makes a nostrconnect:// URI for a signer app to scan.
export function nostrConnect(relay: string): NostrConnect {
  const clientSk = generateSecretKey();
  const secret = Array.from(crypto.getRandomValues(new Uint8Array(12)), (b) => b.toString(16).padStart(2, "0")).join("");
  const uri = createNostrConnectURI({
    clientPubkey: getPublicKey(clientSk),
    relays: [relay],
    secret,
    name: "ncli relay dashboard",
    perms: ["sign_event:27235"],
  });
  return {
    uri,
    wait: async (onAuth, signal) => {
      const pool = new SimplePool();
      try {
        const signer = await BunkerSigner.fromURI(clientSk, uri, { pool, onauth: onAuth }, signal);
        return remote(signer, pool);
      } catch (e) {
        pool.destroy();
        throw e;
      }
    },
  };
}

// isRelayURL accepts the relay URLs a signer can be reached on.
export function isRelayURL(s: string): boolean {
  try {
    const u = new URL(s);
    return u.protocol === "wss:" || (u.protocol === "ws:" && ["localhost", "127.0.0.1"].includes(u.hostname));
  } catch {
    return false;
  }
}
