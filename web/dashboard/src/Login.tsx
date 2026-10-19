import { useEffect, useMemo, useRef, useState } from "react";
import { encode } from "uqr";
import { npubEncode } from "nostr-tools/nip19";
import { ApiError, hasSigner, login, loginWith, setCsrf, setRenewer, type Session } from "./api";
import { canRemember, forgetKey, MIN_PASSWORD, savedKey, saveKey, unlockKey } from "./keystore";
import mark from "./brand/ncli-mark.svg";
import {
  bunkerSigner,
  DEFAULT_SIGNER_RELAY,
  isRelayURL,
  keptKeySigner,
  nostrConnect,
  parseSecretKey,
  secretKeySigner,
} from "./signers";

type Method = "saved" | "extension" | "key" | "remote";

function explain(e: unknown): string {
  if (e instanceof ApiError && e.status === 401) return "This key is not an admin of this relay.";
  if (e instanceof ApiError && e.status === 421) return "This page was opened under a host name the relay doesn't answer to (admin.hosts).";
  return (e as Error).message || String(e);
}

// Qr draws a QR code as one SVG path, on white so phones can scan it in
// either theme.
function Qr({ text }: { text: string }) {
  const path = useMemo(() => {
    const { data, size } = encode(text, { ecc: "M", border: 2 });
    let d = "";
    for (let y = 0; y < size; y++) for (let x = 0; x < size; x++) if (data[y][x]) d += `M${x} ${y}h1v1h-1z`;
    return { d, size };
  }, [text]);
  return (
    <svg className="qr" viewBox={`0 0 ${path.size} ${path.size}`} role="img" aria-label="nostrconnect QR code">
      <rect width={path.size} height={path.size} fill="#fff" />
      <path d={path.d} fill="#000" />
    </svg>
  );
}

function AuthPrompt({ url }: { url: string }) {
  if (!url) return null;
  return (
    <p className="notice">
      Your signer asks you to approve this sign-in:{" "}
      <a href={url} target="_blank" rel="noreferrer noopener">
        open approval page
      </a>
    </p>
  );
}

export function Login({ onSession }: { onSession: (s: Session) => void }) {
  const signer = hasSigner();
  const [saved, setSaved] = useState(savedKey);
  const [method, setMethod] = useState<Method>(saved ? "saved" : signer ? "extension" : "key");
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const [authURL, setAuthURL] = useState("");

  const done = (s: Session) => {
    setCsrf(s.csrf);
    onSession(s);
  };
  const run = async (fn: () => Promise<Session>) => {
    setPending(true);
    setError("");
    try {
      done(await fn());
    } catch (e) {
      setError(explain(e));
    } finally {
      setPending(false);
    }
  };

  return (
    <div className="login">
      <img className="brand" src={mark} alt="" width="48" height="48" />
      <h1>Relay Monitor</h1>
      <p className="muted">Sign in with the relay's admin key. Only a signed login event leaves this page.</p>

      <div className="tabs" role="tablist">
        {(
          [
            ...(saved ? [["saved", "Saved key"]] : []),
            ["extension", "Extension"],
            ["key", "Secret key"],
            ["remote", "Remote"],
          ] as [Method, string][]
        ).map(([m, label]) => (
          <button
            key={m}
            role="tab"
            aria-selected={method === m}
            className={method === m ? "tab active" : "tab"}
            onClick={() => {
              setMethod(m);
              setError("");
              setAuthURL("");
            }}
          >
            {label}
          </button>
        ))}
      </div>

      {method === "extension" && (
        <div className="method">
          <button disabled={!signer || pending} onClick={() => run(login)}>
            {pending ? "Waiting for signer…" : "Sign in with Nostr extension"}
          </button>
          {!signer && <p className="muted">No NIP-07 extension found in this browser.</p>}
        </div>
      )}

      {method === "saved" && saved && (
        <SavedKeyForm
          pubkey={saved.pubkey}
          pending={pending}
          run={run}
          onForget={() => {
            forgetKey();
            setSaved(null);
            setMethod(signer ? "extension" : "key");
          }}
        />
      )}

      {method === "key" && <SecretKeyForm pending={pending} run={run} />}

      {method === "remote" && <RemoteSigner pending={pending} setPending={setPending} run={run} onAuth={setAuthURL} onError={setError} />}

      <AuthPrompt url={authURL} />
      {error && <p className="error" role="alert">{error}</p>}
      <p className="muted small">
        Or run <code>ncli relay dashboard</code>, which signs for you from the relay's config.
      </p>
    </div>
  );
}

// signInWithKey signs in with sk, then keeps it for renewals (keepOpen),
// saves it encrypted (password), or wipes it.
async function signInWithKey(sk: Uint8Array, keepOpen: boolean, password?: string): Promise<Session> {
  const copy = new Uint8Array(sk);
  try {
    const session = await loginWith(secretKeySigner(sk));
    // Only an admin's key gets remembered.
    if (password !== undefined) saveKey(copy, password);
    if (keepOpen) {
      const kept = keptKeySigner(copy);
      setRenewer(kept.sign, kept.release);
    } else {
      copy.fill(0);
    }
    return session;
  } catch (e) {
    copy.fill(0);
    throw e;
  }
}

// yieldFrame lets "Unlocking…" paint before scrypt takes the thread.
const yieldFrame = () => new Promise((r) => setTimeout(r, 30));

function KeepOpen({ value, onChange }: { value: boolean; onChange: (b: boolean) => void }) {
  return (
    <label className="check">
      <input type="checkbox" checked={value} onChange={(e) => onChange(e.target.checked)} /> Stay signed in while this tab is
      open
    </label>
  );
}

function SecretKeyForm({ pending, run }: { pending: boolean; run: (fn: () => Promise<Session>) => void }) {
  const input = useRef<HTMLInputElement>(null);
  const pass = useRef<HTMLInputElement>(null);
  const confirm = useRef<HTMLInputElement>(null);
  const [keepOpen, setKeepOpen] = useState(true);
  const [remember, setRemember] = useState(false);
  return (
    <form
      className="method"
      onSubmit={(e) => {
        e.preventDefault();
        const el = input.current!;
        const raw = el.value;
        // Clear the fields at once: the key lives only in this function.
        el.value = "";
        const pw = remember ? pass.current!.value : undefined;
        const pw2 = remember ? confirm.current!.value : undefined;
        if (pass.current) pass.current.value = "";
        if (confirm.current) confirm.current.value = "";
        run(async () => {
          if (pw !== undefined) {
            if (pw.length < MIN_PASSWORD) throw new Error(`Use a password of at least ${MIN_PASSWORD} characters.`);
            if (pw !== pw2) throw new Error("The passwords don't match.");
          }
          const sk = parseSecretKey(raw);
          if (pw !== undefined) await yieldFrame();
          return signInWithKey(sk, keepOpen, pw);
        });
      }}
    >
      <label htmlFor="sk">nsec or hex secret key</label>
      <input id="sk" ref={input} type="password" autoComplete="off" spellCheck={false} placeholder="nsec1…" required />
      <KeepOpen value={keepOpen} onChange={setKeepOpen} />
      <label className="check">
        <input type="checkbox" checked={remember} disabled={!canRemember()} onChange={(e) => setRemember(e.target.checked)} />{" "}
        Remember on this browser, encrypted with a password
      </label>
      {remember && (
        <>
          <label htmlFor="pw">Password</label>
          <input id="pw" ref={pass} type="password" autoComplete="new-password" minLength={MIN_PASSWORD} required />
          <label htmlFor="pw2">Repeat password</label>
          <input id="pw2" ref={confirm} type="password" autoComplete="new-password" minLength={MIN_PASSWORD} required />
        </>
      )}
      <button type="submit" disabled={pending}>
        {pending ? (remember ? "Encrypting…" : "Signing in…") : "Sign in"}
      </button>
      <p className="muted small">
        {remember
          ? "Saved only as a NIP-49 ncryptsec: unreadable without the password, which is never stored."
          : "Signed here and never sent or stored; leave “remember” off and it is gone when you sign out."}
      </p>
    </form>
  );
}

function SavedKeyForm({
  pubkey,
  pending,
  run,
  onForget,
}: {
  pubkey: string;
  pending: boolean;
  run: (fn: () => Promise<Session>) => void;
  onForget: () => void;
}) {
  const pass = useRef<HTMLInputElement>(null);
  const [keepOpen, setKeepOpen] = useState(true);
  const npub = npubEncode(pubkey);
  return (
    <form
      className="method"
      onSubmit={(e) => {
        e.preventDefault();
        const pw = pass.current!.value;
        pass.current!.value = "";
        run(async () => {
          await yieldFrame();
          return signInWithKey(unlockKey(pw), keepOpen);
        });
      }}
    >
      <p className="saved-who">
        Key for <code title={npub}>{npub.slice(0, 12)}…{npub.slice(-6)}</code>
      </p>
      <label htmlFor="unlock">Password</label>
      <input id="unlock" ref={pass} type="password" autoComplete="current-password" required autoFocus />
      <KeepOpen value={keepOpen} onChange={setKeepOpen} />
      <button type="submit" disabled={pending}>
        {pending ? "Unlocking…" : "Unlock and sign in"}
      </button>
      <button
        type="button"
        className="ghost"
        onClick={() => {
          if (confirm("Forget the key saved on this browser? You'll need the nsec to sign in again.")) onForget();
        }}
      >
        Forget this key
      </button>
    </form>
  );
}

function RemoteSigner({
  pending,
  setPending,
  run,
  onAuth,
  onError,
}: {
  pending: boolean;
  setPending: (b: boolean) => void;
  run: (fn: () => Promise<Session>) => Promise<void>;
  onAuth: (url: string) => void;
  onError: (msg: string) => void;
}) {
  const [mode, setMode] = useState<"bunker" | "connect">("bunker");
  const [bunker, setBunker] = useState("");
  const [relay, setRelay] = useState(DEFAULT_SIGNER_RELAY);
  const [connect, setConnect] = useState<ReturnType<typeof nostrConnect> | null>(null);
  const abort = useRef<AbortController | null>(null);

  useEffect(() => () => abort.current?.abort(), []);

  const viaBunker = () =>
    run(async () => {
      const r = await bunkerSigner(bunker, onAuth);
      try {
        return await loginWith(r.sign);
      } finally {
        await r.close();
      }
    });

  const startConnect = () => {
    if (!isRelayURL(relay)) {
      onError("The relay must be a wss:// URL (ws:// only for localhost).");
      return;
    }
    abort.current?.abort();
    const ctl = new AbortController();
    abort.current = ctl;
    const c = nostrConnect(relay);
    setConnect(c);
    void run(async () => {
      const r = await c.wait(onAuth, ctl.signal);
      try {
        return await loginWith(r.sign);
      } finally {
        await r.close();
      }
    }).finally(() => setPending(false));
  };

  return (
    <div className="method">
      <div className="segmented">
        <label>
          <input type="radio" checked={mode === "bunker"} onChange={() => setMode("bunker")} /> bunker:// URL
        </label>
        <label>
          <input type="radio" checked={mode === "connect"} onChange={() => setMode("connect")} /> Scan with signer app
        </label>
      </div>

      {mode === "bunker" ? (
        <form
          onSubmit={(e) => {
            e.preventDefault();
            void viaBunker();
          }}
        >
          <label htmlFor="bunker">bunker:// URL or NIP-05 name</label>
          <input
            id="bunker"
            value={bunker}
            onChange={(e) => setBunker(e.target.value)}
            autoComplete="off"
            spellCheck={false}
            placeholder="bunker://…?relay=wss://…"
            required
          />
          <button type="submit" disabled={pending}>
            {pending ? "Waiting for approval…" : "Connect and sign in"}
          </button>
        </form>
      ) : (
        <div>
          <label htmlFor="relay">Relay your signer listens on</label>
          <input id="relay" value={relay} onChange={(e) => setRelay(e.target.value)} spellCheck={false} />
          {!connect ? (
            <button onClick={startConnect}>Show code</button>
          ) : (
            <div className="connect">
              <Qr text={connect.uri} />
              <p className="muted small">Scan with Amber, nsec.app or another NIP-46 signer, then approve.</p>
              <div className="copy">
                <input readOnly value={connect.uri} aria-label="nostrconnect URI" onFocus={(e) => e.target.select()} />
                <button type="button" className="ghost" onClick={() => void navigator.clipboard?.writeText(connect.uri)}>
                  Copy
                </button>
              </div>
              {pending && <p className="muted small">Waiting for your signer…</p>}
            </div>
          )}
        </div>
      )}
    </div>
  );
}
