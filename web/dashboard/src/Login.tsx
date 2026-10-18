import { useEffect, useMemo, useRef, useState } from "react";
import { encode } from "uqr";
import { ApiError, hasSigner, login, loginWith, setCsrf, type Session } from "./api";
import {
  bunkerSigner,
  DEFAULT_SIGNER_RELAY,
  isRelayURL,
  nostrConnect,
  parseSecretKey,
  secretKeySigner,
} from "./signers";

type Method = "extension" | "key" | "remote";

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
  const [method, setMethod] = useState<Method>(signer ? "extension" : "key");
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
      <h1>Relay Monitor</h1>
      <p className="muted">Sign in with the relay's admin key. Only a signed login event leaves this page.</p>

      <div className="tabs" role="tablist">
        {(
          [
            ["extension", "Extension"],
            ["key", "Secret key"],
            ["remote", "Remote signer"],
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

function SecretKeyForm({ pending, run }: { pending: boolean; run: (fn: () => Promise<Session>) => void }) {
  const input = useRef<HTMLInputElement>(null);
  return (
    <form
      className="method"
      onSubmit={(e) => {
        e.preventDefault();
        const el = input.current!;
        const raw = el.value;
        // Clear the field at once: the key lives only in this function.
        el.value = "";
        run(async () => loginWith(secretKeySigner(parseSecretKey(raw))));
      }}
    >
      <label htmlFor="sk">nsec or hex secret key</label>
      <input id="sk" ref={input} type="password" autoComplete="off" spellCheck={false} placeholder="nsec1…" required />
      <button type="submit" disabled={pending}>
        {pending ? "Signing in…" : "Sign in"}
      </button>
      <p className="muted small">
        Signed here and discarded: never sent, never stored. An extension or remote signer keeps the key off this
        computer.
      </p>
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
