# CLI narration noise audit

Every progress line printed to stderr while a command runs (per AGENTS.md's
stdout=result / stderr=narration convention), checked against what it
actually needs to say. 22 lines across 8 files. Lines not listed here are
kept as-is (final results, errors, genuine tips).

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

Separate plumbing note, not a wording fix: `ping`'s per-relay
`"connectivity OK"`/`"connectivity check failed"` lines and the final
`"%d of %d reachable"` summary are `ping`'s actual *result* in text mode,
but they go out via `log.Info`/`log.Error` (stderr) instead of stdout —
`cli/ncli/ping.go` itself prints nothing to stdout. Worth revisiting
separately from this noise pass.

## cli/ncli/id.go — vault save (3 lines)

| Line | Before | After |
|---|---|---|
| 204 | `"unlocking vault..."` | removed — redundant with the "Vault password:" prompt right after |
| 206 | `"creating vault identity..."` | removed — redundant with the "Set a vault password:" prompt right after |
| 223 | `"saving identity..."` | removed — superseded by "identity saved to vault (label: %s)" right after |

## cli/ncli/miner.go — progress tick (1 line)

| Line | Before | After |
|---|---|---|
| 177 | `"mining... %s hashes tried, %s elapsed, %s across %d worker(s)"` | `"%s hashes, %s, %s, %d workers"` |

## cli/delegate/command.go — redundant header (1 line)

| Line | Before | After |
|---|---|---|
| 141 | `"Delegation token generated."` | removed — restates the fields printed right below it |

## Clean (audited, zero noise)

`cli/blossom` (all), `cli/bunker/command.go`, `cli/relay/context.go` +
`context_run.go`, `cli/common/errors.go`, `client/publish.go`,
`client/recovery.go`, `client/stream.go`, `client/miner.go`,
`cli/ncli/apply.go`, `dump.go`, `find.go`, `ping.go`, `filters.go`,
`query.go`. `cli/bunker/board.go` and `client/neg_sync.go` are live-TUI
chrome, out of scope here.
