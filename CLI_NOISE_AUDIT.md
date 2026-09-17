# CLI narration noise audit

Every progress line printed to stderr while a command runs (per AGENTS.md's
stdout=result / stderr=narration convention), checked against what it
actually needs to say. 22 lines across 8 files. Lines not listed here are
kept as-is (final results, errors, genuine tips).

Reconciled 2026-09-17 against current source: every file below was read in
full (not re-grepped) and every row cross-checked line-by-line. Result: all
22 rows below still match source verbatim, zero drift, zero reclassified.
The reconciliation pass did surface two things the first pass missed — a
recurring stdout/stderr plumbing bug (see its own section below) and ~38
files that had never actually been checked (folded into "Clean" below).

## cli/reindex/command.go — worst offender (8 lines)

Every reindex phase announces its own start, and the search path reports
its final count twice.

| Line | Before | After |
|---|---|---|
| 40 | `"connecting to search backend..."` | removed |
| 50 | `"starting search reindexing..."` | removed |
| 102 | `"progress..."` (field `indexed`) | `"indexed"` |
| 114 | `"final batch indexed"` (field `indexed`) | removed — duplicates line 124's `total` |
| 122 | `"waiting for verification worker to finish remaining jobs..."` | `"waiting on verification worker"` |
| 128 | `"fetching events..."` | removed |
| 149 | `"starting zap reindexing..."` | removed |
| 157 | `"progress..."` (field `zaps_indexed`) | `"indexed"` |

## cli/relay/service.go — server lifecycle (5 lines)

| Line | Before | After |
|---|---|---|
| 46 | `"server config check"` | removed — fold `pubkey`/`port` fields into line 247 |
| 247 | `"listening..."` (no fields) | `"listening"` with `pubkey`/`port` fields |
| 266 | `"stopping server gracefully"` | removed — outcome already logged by line 280 or 285 |
| 288 | `"stopping verification workers..."` | removed — paired with "verification workers stopped" |
| 292 | `"stopping events store..."` | removed — paired with "events store stopped" |

## cli/relay/admin.go + command.go — duplicated across both (2 lines)

| Line | Before | After |
|---|---|---|
| admin.go:172 | `"using config file"` (field `config`) | `"config"` |
| command.go:286 | `"using config file"` (field `config`) | `"config"` |

## client/client.go — per-target query noise (4 lines)

| Line | Before | After |
|---|---|---|
| 149 | `"querying %s"` (local path, in mergeEventsFromTargets) | removed |
| 156 | `"querying %s"` (remote host, in mergeEventsFromTargets) | removed |
| 225 | `"querying %s"` (local path, in Find) | removed |
| 232 | `"querying %s"` (remote host, in Find) | removed |

## client/ping.go — start-of-run announcement (1 line)

| Line | Before | After |
|---|---|---|
| 123 | `"Checking connectivity for %d %s"` | removed — the real result is the "%d of %d reachable" summary at line 167 |

## cli/ncli/id.go — vault save (3 lines)

| Line | Before | After |
|---|---|---|
| 204 | `"unlocking vault..."` | removed — redundant with the "Vault password:" prompt right after (confirmed live in `cli/keyresolve/resolve.go`) |
| 206 | `"creating vault identity..."` | removed — redundant with the "Set a vault password:" prompt right after (same) |
| 223 | `"saving identity..."` | removed — superseded by "identity saved to vault (label: %s)" right after |

## cli/ncli/miner.go — progress tick (1 line)

| Line | Before | After |
|---|---|---|
| 177 | `"mining... %s hashes tried, %s elapsed, %s across %d worker(s)"` | `"%s hashes, %s, %s, %d workers"` |

## cli/delegate/command.go — redundant header (1 line)

| Line | Before | After |
|---|---|---|
| 141 | `"Delegation token generated."` | removed — restates the fields printed right below it |

## Stdout/stderr plumbing gaps (separate issue — not wording)

Three commands report their actual text-mode *result* — not narration —
through `log.Info`/`log.Error`, which AGENTS.md routes to stderr. Nothing
wrong with the wording; the fix is which stream it goes to.

| File | Lines | Text | Command affected |
|---|---|---|---|
| `client/ping.go` | ~167, ~214–222 | `"%d of %d %s reachable"`, per-relay `"connectivity OK"`/`"connectivity check failed"` | `ping`'s entire text-mode result lives on stderr |
| `cli/ncli/prefs.go` | 60, 62, 93, 95, 146 | `"added"`, `"already configured"`, `"removed"`, `"not configured"`, `"cleared"` | `prefs relays add`/`remove`/`clear` |
| `cli/relay/context_run.go` | 146, 189 | `"relay context created"`, `"new identity saved to vault"` | `relay --context <new-name>`'s auto-create path |

## Clean (audited in full, zero noise)

Every file below was read end to end, not sampled by grep.

- **cli/blossom** (all 9): `command.go`, `download.go`, `list.go`, `mirror.go`, `report.go`, `rm.go`, `servers.go`, `shared.go`, `upload.go` — no progress narration anywhere, including upload/download/mirror's transfer loops.
- **cli/bunker** (all 16 non-TUI files): `client.go`, `clipboard.go`, `command.go`, `daemon.go`, `eventlog.go`, `grantspec.go`, `handler.go`, `identity.go`, `ipc_client.go`, `ipc_server.go`, `policy.go`, `queue.go`, `spawn.go`, `spawn_unix.go`, `spawn_windows.go`, `uri.go`. `daemon.go`'s custom `d.log()` method never reaches CLI stdout/stderr — it only feeds an in-memory TUI log panel or a rotating `daemon.log` file on disk.
- **cli/relay**: `context.go`, `context_run.go` (noise-wise; see plumbing gap above), `service_membership.go`.
- **cli/common** (all 10): `errors.go`, `appdir.go`, `args.go`, `auth.go`, `config.go`, `logging.go`, `logging_unix.go`, `logging_windows.go`, `prompt.go`, `version.go`.
- **cli/keyresolve/resolve.go**, **cli/reindex/state.go**.
- **cli/ncli** (11): `apply.go`, `decode.go`, `dump.go`, `filters.go`, `find.go`, `id_sign.go`, `ping.go`, `publish.go`, `query.go`, `root.go`, `version.go`. (`prefs.go` has the plumbing gap above instead.)
- **client/** (12): `decode.go`, `event_export.go`, `identity.go`, `inspect.go`, `inspect_store.go`, `miner.go`, `prefs.go`, `publish.go`, `recovery.go`, `spec.go`, `stream.go`, `vault.go`.

Out of scope, not "clean" — confirmed to have no headless code path at all,
so there's no console narration to audit: `cli/bunker/board.go` (live TUI
board) and `client/neg_sync.go` (`Client.init()` rejects a `SyncSpec`
without a TUI attached).
