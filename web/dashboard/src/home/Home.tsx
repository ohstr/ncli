import { useEffect, useState } from "react";
import { npubEncode } from "./bech32";
import { HIGHLIGHTS, NIP_TITLES, nipLink } from "./nips";
import { contactLink, fetchInfo, iconCandidates, query, relayURL, safeURL, type Info, type NostrEvent } from "./relay";
import mark from "../brand/ncli-mark.svg";

interface Profile {
  name?: string;
  display_name?: string;
  picture?: string;
  banner?: string;
  about?: string;
  nip05?: string;
}

interface Group {
  id: string;
  name: string;
  picture: string;
  about: string;
}

function tag(ev: NostrEvent, name: string): string {
  return ev.tags.find((t) => t[0] === name)?.[1] ?? "";
}

function toGroups(events: NostrEvent[]): Group[] {
  return events
    .filter((e) => !e.tags.some((t) => t[0] === "private"))
    .map((e) => ({ id: tag(e, "d"), name: tag(e, "name") || tag(e, "d"), picture: safeURL(tag(e, "picture")), about: tag(e, "about") }))
    .filter((g) => g.id);
}

function bytes(n?: number): string {
  if (!n) return "";
  if (n >= 1 << 20) return `${(n / (1 << 20)).toFixed(n % (1 << 20) ? 1 : 0)} MiB`;
  if (n >= 1024) return `${Math.round(n / 1024)} KiB`;
  return `${n} B`;
}

type Status = { state: "checking" } | { state: "online"; ms: number } | { state: "offline"; why: string };

function CopyField({ value, label }: { value: string; label: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <div className="copyfield">
      <code aria-label={label}>{value}</code>
      <button
        onClick={() => {
          void navigator.clipboard?.writeText(value).then(() => {
            setCopied(true);
            setTimeout(() => setCopied(false), 1500);
          });
        }}
      >
        {copied ? "Copied" : "Copy"}
      </button>
    </div>
  );
}

function Access({ lim }: { lim: Info["limitation"] }) {
  const items: { title: string; body: string; tone: "open" | "gate" }[] = [];
  if (lim?.membership_required) {
    items.push({ title: "Members only", body: "Reading and posting need membership. Ask the operator for an invite.", tone: "gate" });
  } else if (lim?.auth_required) {
    items.push({ title: "Sign-in required", body: "Your client signs in with your key (NIP-42) before reading or posting.", tone: "gate" });
  } else {
    items.push({ title: "Open", body: "Anyone can read and post.", tone: "open" });
  }
  if (lim?.min_pow_difficulty) {
    items.push({ title: `Proof of work ≥ ${lim.min_pow_difficulty}`, body: "Posts must be mined to this difficulty (NIP-13).", tone: "gate" });
  }
  return (
    <div className="access">
      {items.map((i) => (
        <div key={i.title} className={`access-item ${i.tone}`}>
          <strong>{i.title}</strong>
          <span>{i.body}</span>
        </div>
      ))}
    </div>
  );
}

function Limits({ lim }: { lim: Info["limitation"] }) {
  const rows: [string, string][] = [];
  if (lim?.max_message_length) rows.push(["Largest message", bytes(lim.max_message_length)]);
  if (lim?.max_subscriptions) rows.push(["Subscriptions per connection", String(lim.max_subscriptions)]);
  if (lim?.max_limit) rows.push(["Events per request", lim.max_limit >= 100_000 ? "no limit" : String(lim.max_limit)]);
  if (lim?.max_indexable_tags) rows.push(["Indexed tags per event", String(lim.max_indexable_tags)]);
  if (!rows.length) return null;
  return (
    <dl className="limits">
      {rows.map(([k, v]) => (
        <div key={k}>
          <dt>{k}</dt>
          <dd>{v}</dd>
        </div>
      ))}
    </dl>
  );
}

export function Home() {
  const [info, setInfo] = useState<Info | null>(null);
  const [error, setError] = useState("");
  const [status, setStatus] = useState<Status>({ state: "checking" });
  const [profile, setProfile] = useState<Profile | null>(null);
  const [groups, setGroups] = useState<Group[]>([]);
  const [showAll, setShowAll] = useState(false);
  // Image URLs that failed to load, so the icon falls through to the next one.
  const [broken, setBroken] = useState<ReadonlySet<string>>(new Set());
  const url = relayURL();
  const icon = info ? iconCandidates(info.icon, profile?.picture).find((u) => !broken.has(u)) ?? "" : "";
  const markBroken = (u: string) => setBroken((b) => new Set(b).add(u));

  useEffect(() => {
    fetchInfo()
      .then((i) => {
        setInfo(i);
        if (i.name) document.title = i.name;
      })
      .catch((e) => setError((e as Error).message));
  }, []);

  useEffect(() => {
    if (!info) return;
    const pk = info.pubkey ?? "";
    const restricted = info.limitation?.auth_required || info.limitation?.membership_required;
    // One REQ checks the relay answers and fetches the operator's profile.
    query(url, /^[0-9a-f]{64}$/.test(pk) ? { kinds: [0], authors: [pk], limit: 1 } : { limit: 0 })
      .then((p) => {
        setStatus({ state: "online", ms: p.latencyMs });
        const ev = p.events[0];
        if (ev) {
          try {
            setProfile(JSON.parse(ev.content));
          } catch {
            /* not JSON */
          }
        }
      })
      .catch((e) => setStatus({ state: "offline", why: (e as Error).message }));
    const nips = (info.supported_nips ?? []).map(String);
    if (nips.includes("29") && !restricted) {
      query(url, { kinds: [39000], limit: 24 })
        .then((p) => setGroups(toGroups(p.events)))
        .catch(() => {});
    }
  }, [info, url]);

  if (error) {
    return (
      <main className="home">
        <h1>Nostr relay</h1>
        <p className="muted">{url}</p>
        <p className="error">Couldn't read this relay's information: {error}</p>
      </main>
    );
  }
  if (!info) return <main className="home" aria-busy="true" />;

  const nips = (info.supported_nips ?? []).map(String);
  const banner = safeURL(profile?.banner);
  const npub = npubEncode(info.pubkey ?? "");
  const contact = contactLink(info.contact ?? "");
  const highlights = HIGHLIGHTS.filter((h) => nips.includes(h.nip));
  const operator = profile?.display_name || profile?.name;

  return (
    <main className="home">
      <header className="hero">
        {banner && <img className="banner" src={banner} alt="" referrerPolicy="no-referrer" />}
        <div className="hero-body">
          {icon ? <img key={icon} className="icon" src={icon} alt="" referrerPolicy="no-referrer" onError={() => markBroken(icon)} /> : <div className="icon placeholder">{(info.name || "R").slice(0, 1).toUpperCase()}</div>}
          <div className="hero-text">
            <h1>{info.name || "Nostr relay"}</h1>
            <span className={`status ${status.state}`} role="status">
              {status.state === "checking" && "Checking…"}
              {status.state === "online" && `Online · ${status.ms} ms`}
              {status.state === "offline" && `Unreachable: ${status.why}`}
            </span>
          </div>
        </div>
        {info.description && <p className="description">{info.description}</p>}
        <div className="connect">
          <span className="label">Add this relay to your Nostr client</span>
          <CopyField value={url} label="Relay address" />
        </div>
      </header>

      <section>
        <h2>Who can use it</h2>
        <Access lim={info.limitation} />
        <Limits lim={info.limitation} />
      </section>

      {highlights.length > 0 && (
        <section>
          <h2>What it does</h2>
          <ul className="highlights">
            {highlights.map((h) => (
              <li key={h.nip}>
                <strong>{h.label}</strong>
                <span>{h.hint}</span>
              </li>
            ))}
          </ul>
        </section>
      )}

      {groups.length > 0 && (
        <section>
          <h2>Groups</h2>
          <ul className="groups">
            {groups.map((g) => (
              <li key={g.id}>
                {g.picture ? <img src={g.picture} alt="" referrerPolicy="no-referrer" /> : <span className="group-icon">#</span>}
                <div>
                  <strong>{g.name}</strong>
                  {g.about && <span>{g.about}</span>}
                </div>
              </li>
            ))}
          </ul>
        </section>
      )}

      <section>
        <h2>
          Supported NIPs <span className="count">{nips.length}</span>
        </h2>
        <ul className={`nips ${showAll ? "all" : ""}`}>
          {nips.map((n) => (
            <li key={n}>
              <a href={nipLink(n)} target="_blank" rel="noreferrer noopener">
                <span className="nip">{n}</span>
                <span>{NIP_TITLES[n] ?? `NIP-${n}`}</span>
              </a>
            </li>
          ))}
        </ul>
        {nips.length > 12 && (
          <button className="ghost" onClick={() => setShowAll((s) => !s)}>
            {showAll ? "Show fewer" : `Show all ${nips.length}`}
          </button>
        )}
      </section>

      <section>
        <h2>Operator</h2>
        <div className="operator">
          {safeURL(profile?.picture) && <img src={safeURL(profile?.picture)} alt="" referrerPolicy="no-referrer" />}
          <div>
            {operator && <strong>{operator}</strong>}
            {profile?.nip05 && <span className="muted">{profile.nip05}</span>}
            {profile?.about && <p>{profile.about}</p>}
            {npub && <CopyField value={npub} label="Operator npub" />}
            {contact && (
              <p className="muted">
                Contact:{" "}
                {contact.href ? (
                  <a href={contact.href} target="_blank" rel="noreferrer noopener">
                    {contact.label}
                  </a>
                ) : (
                  contact.label
                )}
              </p>
            )}
          </div>
        </div>
      </section>

      <footer>
        <img className="by" src={mark} alt="" width="16" height="16" />
        {info.software ? (
          <a href={safeURL(info.software) || "https://github.com/ohstr/ncli"} target="_blank" rel="noreferrer noopener">
            ncli
          </a>
        ) : (
          "ncli"
        )}
        {info.version && <span> {info.version}</span>}
      </footer>
    </main>
  );
}
