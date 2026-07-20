# ncli relay stats/reindex/clear — full reference

<!-- Mirrors ohstr/ncli's cli/relay/admin.go, cli/reindex/command.go, and
cli/relay/service.go as of writing. Update by hand if these change. -->

## HTTP endpoints (what `ncli relay stats`/`reindex`/`clear` call, on the relay itself)

All require a NIP-98 `Authorization` header signed with `nip11.privkey`,
verified against `nip11.pubkey`.

| Method | Path | Triggered by | Response |
|---|---|---|---|
| `GET` | `/admin/worker/stats` | `ncli relay stats` | JSON: verification worker stats + search/zap reindex status |
| `POST` | `/admin/reindex/search` | `ncli relay reindex search` | `202` + `{"status":"started"}`, or `409` + status JSON if already running |
| `POST` | `/admin/reindex/zaps` | `ncli relay reindex zaps` | Same pattern as search |
| `DELETE` | `/admin/search` | `ncli relay clear search` | `200` + `{"status":"deleted"}` |
| `DELETE` | `/admin/zaps` | `ncli relay clear zaps` | `200` + `{"status":"deleted"}` |

`POST` reindex endpoints run the actual work in a background goroutine
inside the relay process and return immediately — they do not block for
completion. The endpoint paths themselves still say `/admin/...` — only the
CLI's subcommand path dropped the "admin" segment, not the HTTP API.

## `ncli relay stats`/`reindex`/`clear` subcommands

```
ncli relay stats
ncli relay reindex search
ncli relay reindex zaps
ncli relay clear search
ncli relay clear zaps
```

Persistent `--json` flag on every subcommand, prints the relay's raw JSON
response instead of formatted text. Auth config is read the same way as
`ncli relay`: `port` (default `5500`) and `nip11.privkey` (required — errors
immediately if absent, no network call attempted) from `--config`.

There is no offline reindex command — `search`/`zaps` reindexing only
happens through a running relay via the endpoints above. Unlike these
commands' own connection settings (`port`/`nip11.privkey`, read from
`--config` before the request is sent), `cache.search.host`/`key`/
`index_name` for the reindex job itself come from the *relay's* live config
(the `/admin/reindex/search` handler reads them from viper at request time)
— if `cache.search.host` is unset there, the reindex goroutine fails trying
to connect to an empty Meilisearch URL rather than with a friendly
validation error.

## Single-flight behavior

The `reindex` package's package-level `SearchState`/`ZapsState` in-memory
flags (`TryStart`/`Complete`) protect against two concurrent reindex jobs of
the same kind *within the relay process* — e.g. two rapid `ncli relay
reindex search` HTTP calls return `409 Conflict` on the second one rather
than running both. This only guards the relay's own process; it doesn't
reach across separate relay instances pointed at the same `store` file.
