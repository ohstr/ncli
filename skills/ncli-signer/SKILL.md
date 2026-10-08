---
name: ncli-signer
description: Run ncli as a local policy signer on a unix socket (ncli signer serve/status/check) and sign through it (ncli id sign --signer / ncli publish --signer with a bunker+unix:// URI). Use when keeping a Nostr key out of an AI agent's container (Kubernetes sidecar), writing or debugging a signer-policy file (selectors, deny_matching, rate limits, attestations such as a maintainer's "approve <id>"), wiring a client to a bunker+unix:// signer, or figuring out why a signing request was denied.
license: Unlicense
---

<!-- Mirrors ohstr/ncli's signer/ and cli/signer/ as of writing. Update by
hand if flags/behavior change. -->

# ncli signer

`ncli signer serve` holds one Nostr key in memory and signs for other
processes over a unix socket, as a policy file allows (default deny). It
speaks the NIP-46 method set as newline-delimited JSON, with no relay and
no encryption. The socket's file permissions are the authentication.

Use it when a process that might be compromised (an LLM agent) needs
signatures but must never hold the key. For a signer reached over relays
with a human approving each request, use `ncli bunker` (skill
`ncli-bunker`) instead.

## Run it

```sh
# key from an ncryptsec (NIP-49) file; password from a file or NCLI_VAULT_PASSWORD
ncli signer serve --socket /run/signer/agent.sock \
  --identity file:/etc/signer/key.ncryptsec --password-file /etc/signer/password \
  --policy policy.yaml --state-dir /var/lib/signer

# key from the vault (saved with `ncli id import`)
NCLI_VAULT_PASSWORD=... ncli signer serve --socket /run/signer/agent.sock \
  --identity agent-key --policy policy.yaml --state-dir /var/lib/signer
```

- **Key sources.** `--identity` takes a vault label or `file:<path>` (an
  ncryptsec file). Without it, serve falls back to
  `NCLI_SIGNER_IDENTITY`, then to the vault's only entry. A plaintext
  `nsec`/hex key is refused, both as an argument and in a file
  (`invalid_input`).
- **`--state-dir`** is required when the policy uses attestations. It
  persists the ids of used attestations, so a restart can't replay them.
- **The socket** defaults to mode `0660`. `--socket-uid`/`--socket-gid`
  chown it. `--allow-uid`/`--allow-gid` (repeatable, Linux only) drop any
  other caller, checked by `SO_PEERCRED`'s uid and *primary* gid.
  Supplementary groups are not seen.
- **Reloading.** `SIGHUP` reloads the policy and its `authors_file`s.
  `--watch` reloads when their content changes, which catches Kubernetes
  ConfigMap swaps. A bad file keeps the active policy and logs the error.
- **`--allow-catch-all`** permits an `allow` rule with an empty selector.
  Without it, such a policy is refused at load.
- **Logs.** stdout carries one JSON line per decision:
  `{time, req_id, method, kind, event_id, client:{uid,gid,pid,conn}, counterpart, decision, rule, reason, attestations}`.
  `client.conn` numbers connections since start. Clients number their own
  requests, so `req_id` alone repeats across connections.
  It never includes content or key material. `--denials-file` also
  appends denials alone. `ping`, `get_public_key` and `signer_status`
  are not logged. Narration goes to stderr.
- **Already running.** A second serve on a live socket fails with
  `conflict` (exit 5). A stale socket file is replaced, and a symlink at
  the path is refused.

## Sign through it

```sh
ncli id sign --signer bunker+unix:///run/signer/agent.sock -e draft.json -o signed.json --json
# a rule that requires an approval: pass the signed approval event(s)
ncli id sign --signer bunker+unix:///run/signer/agent.sock -e state.json -o signed.json --attestations approval.json
# signs every event without a sig, then publishes
ncli publish -e draft.json --signer bunker+unix:///run/signer/agent.sock -s wss://relay.example
```

- **URI forms.** `bunker+unix:///abs/path` is canonical, and
  `unix:///abs/path` also works. The path must be absolute.
- **Choosing a signer.** `--signer` also accepts a plain identity, so
  scripts can switch between local and socket signing with one flag.
  `id sign` takes `--identity` or `--signer`, not both.
- **Exit codes.**

  | Exit | Code | Meaning |
  |---|---|---|
  | 7 | `auth` | The policy denied the request; the message carries the reason. |
  | 6 | `network` | The socket is missing or the signer is down. |
  | 3 | `invalid_input` | The signer rejected the request as malformed. |

- **Event id.** The signer sets the event's `pubkey` to its own key and
  computes the id. An attestation that names the target id must name
  *that* id, computed with the signer's pubkey. One way to get it:
  `ncli signer check --pubkey <signer npub> -e draft.json --json` prints
  `event_id`.

## Probe and dry-run

```sh
# exits 0 with pubkey/policy hash/uptime; 6 when nothing answers (use as a k8s probe)
ncli signer status --socket /run/signer/agent.sock --json
# offline: exits 7 if any event is denied; --now/--start-time pin time for CI fixtures
ncli signer check --policy policy.yaml -e events.json --pubkey npub1... --json
ncli signer check --policy policy.yaml -e state.json --attestations approval.json \
  --pubkey npub1... --now 2026-10-08T12:00:00Z --start-time 2026-10-08T11:00:00Z
ncli signer check --policy policy.yaml --method nip44_decrypt --counterpart npub1... --pubkey npub1...
```

`check` cannot reproduce two things the live signer does:
- **Key guard.** It has no private key, so the built-in key guard is
  skipped.
- **Used set.** It starts with an empty used-attestation set, so it
  won't catch a replay.

## Policy in brief

```yaml
kind: signer-policy
max_clock_skew: 10m
rules:
  - name: chat
    allow: {kinds: [9, 7, 1111]}
    deny_matching: ['nsec1[02-9ac-hj-np-z]{58}', 'ncryptsec1']
    rate: 60/m
  - name: repo-state
    allow: {kinds: [30617, 30618]}
    max_clock_skew: 2m
    require:
      attestations:
        - name: maintainer-approval
          kinds: [9]
          authors_file: approvers.txt
          max_age: 15m
          binds: [{content: "approve {id}"}]
  - name: everything-else
    deny: {}
```

- **Evaluation.** The first rule whose selector matches decides. Inside
  a matched allow rule, a `deny_matching` hit, a missing or invalid
  attestation, or the rate limit all deny. None of them fall through to
  a later rule. A request that matches no rule is denied.
- **Always on.**
  - An event or plaintext containing the signer's own key is denied.
  - An attestation signed by the signer's key never counts.
  - An attestation created before the signer started never counts.
  - An attestation is single-use.
- **Always allowed.** `get_public_key`, `ping`, `signer_status`, and
  no-op acks for `connect`, `get_relays`, `switch_relays` and `logout`.

[references/policy.md](references/policy.md) is the full reference:
selectors, binds and templates, target-age rules, and why to scope auth
kinds. [references/kubernetes.md](references/kubernetes.md) covers the
sidecar layout.

## Threat model in one breath

A client with socket access can sign whatever the policy allows, up to
the rate limits. It cannot read the key, cannot get the key into a signed
event, cannot satisfy an attestation (which needs another key), and
cannot replay one.

Keep the signer in a separate container with a separate uid and
`shareProcessNamespace: false`. A process that can read the signer's
memory has the key.

## Gotchas

- **Don't print the key you're about to protect.** Under `--json`,
  `ncli id --save --label agent-key` prints the new `nsec` and
  `priv_hex`. An agent creating its own signer key should keep only
  public fields: `ncli id --save --label agent-key --json | jq '{npub,
  pub_hex}'`. Better still, have the operator create the key.

- **Scope auth kinds.** An unscoped 22242 (NIP-42) or 27235 (NIP-98)
  rule lets the agent log in as this key on any relay or HTTP service.
  Always pin them with `tags` (`relay`, `u`, `method`).
- **Tag-only binds.** A `{tag: d, from: "tag:d"}` bind ties the
  attestation to the repo name only, so any old label on that repo
  satisfies it. Use `{id}` or `from: id` to bind the exact event.
- **Restart window.** The "created before start" check allows
  `max_clock_skew`. If `--state-dir` was lost, an attestation made in
  that window just before a restart can validate once. Keep the skew
  small on attestation rules.
- **Restarts and approvals.** After any restart, approvals issued before
  it must be re-issued.
- **Request ids.** One connection handles requests in order, so match
  responses by `id`.
