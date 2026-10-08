# Kubernetes sidecar

The agent and the signer run as two containers in one pod:

- **The socket** is on an `emptyDir` shared by both containers
  (`/run/signer`).
- **`--state-dir`** (used attestations) is on a PVC `subPath`, mounted
  only in the signer container (`/var/lib/signer`). An `emptyDir` would
  be wiped on every pod recreation.
- **The key Secret** (`key.ncryptsec` and `password`) is mounted only in
  the signer container.
- **The policy and `approvers.txt`** come from a ConfigMap, with
  `serve --watch`, so edits apply without a restart.

The full manifest is `examples/signer/pod.yaml` in the ncli repo.

## Key material

```sh
# key.ncryptsec: from any NIP-49 tool (e.g. nak's `key encrypt`)
kubectl create secret generic agent-signer-key \
  --from-file=key.ncryptsec --from-file=password
kubectl create configmap agent-signer-policy \
  --from-file=agent-policy.yaml --from-file=approvers.txt
```

`serve` refuses a plaintext nsec, as an argument or in a file. You can
instead mount a vault directory made with `ncli id import`, set
`NCLI_VAULT_PASSWORD`, and use `--identity <label>`.

## Isolation checklist

- **Separate uids.** For example, the signer runs as 10001 and the agent
  as 1000.
- **Socket access.** `fsGroup: 2000` puts both containers in group 2000.
  The signer runs with `--socket-gid=2000`, and the socket mode stays
  `0660`.
- **Caller check.** `--allow-uid=1000` drops any connection not from the
  agent's uid. It is checked with `SO_PEERCRED`, which reports the uid
  and the *primary* gid only.
- **No shared processes.** Keep `shareProcessNamespace: false`. With it
  on, a process that can see the signer's PID may read its memory.
- **Hardened signer.** Give the signer container
  `readOnlyRootFilesystem: true`, `allowPrivilegeEscalation: false` and
  `capabilities: {drop: [ALL]}`.
- **Secret mode.** With `fsGroup`, Secret files are `root:<fsGroup>`, so
  use `defaultMode: 0440`. Only the signer mounts the Secret.
- **Agent mounts.** The agent mounts only the socket volume: no key, no
  state, no policy.

## Probes

```yaml
readinessProbe:
  exec:
    command: [ncli, signer, status, --socket, /run/signer/agent.sock]
```

`status` exits 6 when nothing answers.

## Agent side

```sh
export NOSTR_SIGNER=bunker+unix:///run/signer/agent.sock
ncli id sign --signer "$NOSTR_SIGNER" -e draft.json -o signed.json
ncli publish -e draft.json --signer "$NOSTR_SIGNER"
```

Tools that speak NIP-46 can reuse their request code: write one request
JSON per line to the socket, unencrypted and without the event wrapper,
then read one response line per request.

## Upgrades and restarts

- **Restarts and approvals.** A restart, including a pod recreated by a
  `Recreate` rollout, invalidates approvals issued before it. That is the
  built-in "created before the signer started" check.
- **Replay.** Used approvals stay used, because the state lives on the
  PVC.
- **What to watch.** `status` reports `policy_sha256`, so you can check
  that a ConfigMap change was applied. The stdout decision log goes to
  the container log. Ship `denials.ndjson` to wherever you alert.
