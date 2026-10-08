# signer-policy reference

```yaml
kind: signer-policy        # required
max_clock_skew: 10m        # default 10m; per-rule override below
rules:                     # tried in order; first match decides; no match = deny
  - name: <unique>
    allow: <selector>      # exactly one of allow / deny
    deny: <selector>
    deny_matching: [<regexp>, ...]   # allow rules only
    rate: <N>/s|m|h                  # allow rules only; token bucket per rule
    max_clock_skew: <duration>       # allow rules only
    require:                         # allow rules limited to sign_event only
      attestations: [<attestation>, ...]   # all required, each by a distinct event
```

Unknown fields are a load error. So is an `allow` rule with an empty
selector, unless serve/check runs with `--allow-catch-all`.

## Selector

| Field | Applies to | Matches when |
|---|---|---|
| `methods` | all | the method is listed: `sign_event`, `nip04_encrypt`, `nip04_decrypt`, `nip44_encrypt`, `nip44_decrypt` |
| `kinds` | `sign_event` | the event kind is listed |
| `tags` | `sign_event` | for every key, some tag with that name has a first value matching an entry: exact, or a prefix when the entry ends in `*` |
| `counterparts` | encrypt/decrypt | the peer pubkey (npub or hex) is listed |

- **Default methods.** If `methods` is omitted, `kinds`/`tags` imply
  `[sign_event]`, and `counterparts` implies all four encrypt/decrypt
  methods.
- **No mixing.** Combining `kinds`/`tags` with `counterparts` in one
  selector is a load error. Split them into two rules.
- **Empty selector.** `deny: {}` matches everything, which makes it the
  usual last rule.

`deny_matching` checks a `sign_event`'s content and every tag value, or an
`*_encrypt` request's plaintext. The reason names the pattern by its
index (`pattern #2`), not its text.

## Scope authentication kinds

NIP-42 (22242) and NIP-98 (27235) events are login credentials. An
unscoped `allow: {kinds: [22242, 27235]}` lets a compromised client log
in as the signer's key on *any* relay or HTTP service. Pin them:

```yaml
  - name: relay-auth
    allow:
      kinds: [22242]
      tags: {relay: ["wss://relay.example", "wss://relay.example/*"]}
  - name: http-auth
    allow:
      kinds: [27235]
      tags: {u: ["https://git.example/*"], method: ["GET", "POST"]}
```

- **Exact host.** List the bare host exactly, *and* with `/*`. A bare
  prefix such as `wss://relay.example*` would also match
  `wss://relay.example.evil.example`.
- **AND across keys.** All tag keys must match. In the example above,
  `method: DELETE` on the git host is denied.

## Attestations

An attestation is another signed event the request must carry: a human
approval, a CI result, and so on. Clients send them as `sign_event`
`params[1]`, a JSON array of events (`ncli id sign --attestations
file.json`).

```yaml
- name: maintainer-approval
  kinds: [9]                     # required
  authors: [npub1..., <hex>]     # and/or
  authors_file: approvers.txt    # one npub/hex per line, # comments; relative to the policy file
  max_age: 15m                   # required, at most 24h
  binds: [<bind>, ...]           # required, all must hold
```

Checks run in this order, and the deny reason names the first failure:
1. The signature verifies (`bad signature`).
2. The author is not the signer's own key, even if listed
   (`signed by the signer itself`).
3. The kind is listed (`wrong kind N`).
4. The author is listed (`author not allowed`).
5. It is not dated later than now plus `max_clock_skew`
   (`future-dated`).
6. It is no older than `max_age` (`stale`).
7. It was created no earlier than the signer's start minus
   `max_clock_skew` (`created before the signer started`).
8. It has not been used before (`already used`).
9. Every bind holds (`bind #N not satisfied: ...`).
10. An id-bound attestation is not older than the target
    (`predates the target event`).

A malformed `authors_file` line is a load error. On reload the old policy
stays.

### Binds

| Bind | Holds when the attestation... |
|---|---|
| `{tag: T, from: id}` | has `[T, <target id>]` |
| `{tag: T, from: "tag:U"}` | has `[T, v]` for every value `v` of the target's `U` tag |
| `{content: "<template>"}` | content equals the template, after trimming whitespace |
| `{content_contains: "<template>"}` | content contains the template |

- **Placeholders.** Templates can use `{id}` (the target id) and
  `{tag:U}`, which expands once per value of the target's `U` tag; every
  expansion must hold. Any other placeholder is a load error.
- **Missing tag.** If the target has no `U` tag, the bind fails.
- **The target id** is computed by the signer with its own pubkey. For a
  draft, `ncli signer check --pubkey <signer> -e draft.json --json`
  prints it as `event_id`.

**Bind the event, not just a tag.** `{tag: d, from: "tag:d"}` alone ties
the attestation to the repo name, not to a commit or an event, so any old
label on that repo satisfies it. That is not a CI gate. Use `from: id` or
`{id}` for anything that must approve one specific change.

### Target age

A `sign_event`'s `created_at` is checked inside the matched rule:
- **Future.** It may never be more than the rule's `max_clock_skew`
  ahead of the signer's clock.
- **Past, by default.** It may be at most `max_clock_skew` behind.
- **Past, with an id-bound attestation.** If the rule's required
  attestation binds the target id (`from: id` or `{id}`) and is
  satisfied, the target may be as old as that attestation's `max_age`.
  A human who approves 8 minutes after the draft was made doesn't make
  it too old.

## Replay protection

- **Within a run.** Used attestation ids go to
  `<state-dir>/used-attestations.ndjson` and are fsynced before the
  signature is returned. If that write fails, the request is denied.
- **Across restarts.** An attestation created before the signer started
  (minus `max_clock_skew`) never counts. This covers a lost state dir:
  approvals issued before a restart must be re-issued. One that falls
  within the skew can still validate once, so keep `max_clock_skew`
  small on attestation rules.
- **Pruning.** Entries are pruned after 24h plus one hour, which is
  safe because of the 24h `max_age` cap.

## Rate limits

`rate: 60/m` gives a burst of 60 that refills at 60 per minute, with one
bucket per rule. A rate change on reload resets that rule's bucket.
