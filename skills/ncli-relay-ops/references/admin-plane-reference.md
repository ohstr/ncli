# ncli relay admin listener — full reference

<!-- Mirrors ohstr/ncli's cli/relay/adminplane/, cli/relay/jobs/,
cli/relay/metrics/ and cli/relay/admin_api.go as of writing. Update by hand
if these change. -->

## Config (`admin:` in relay.yaml)

| Key | Default | Notes |
|---|---|---|
| `listen` | `127.0.0.1:<port+1>` | `off` disables the listener. Must not equal `port`. Non-loopback requires `tls`. |
| `tls.cert`, `tls.key` | — | Both or neither. The CLI trusts `tls.cert` when it connects. |
| `tls.clientCA` | — | Requires every client to present a certificate it signed (mTLS). |
| `allowCIDRs` | — | Refuse other source addresses (`403`) before any auth. A bare IP is a /32 or /128. |
| `hosts` | — | Extra `Host` values to answer, e.g. a reverse proxy's name. The listen address (and on loopback `localhost`/`127.0.0.1`/`[::1]` at that port) always count; any other Host gets `421`. Required when `listen` is `0.0.0.0`/`::`. |
| `dashboard.enabled` | `false` | Serves `/admin/ui/` and `POST /admin/api/login`. Needs an admin pubkey. |
| `dashboard.sessionTTL` | `15m` | 1m–24h. |
| `metrics.enabled` | `false` | Serves `/metrics`. Needs `tokensFile`. |
| `metrics.tokensFile` | — | Lines `sha256:<hex> name=<n> created=<date>`; mode must deny group/other. |
| `metrics.scrapeCacheTTL` | `5s` | A rendering of `/metrics` is reused this long (≤ 1m). |

Validation failures stop the relay at startup: `usage` (exit 2) for a
missing or contradictory setting, `invalid_input` (exit 3) for a bad value.

## Routes

| Method | Path | Credential | Answer |
|---|---|---|---|
| `GET` | `/metrics` | `Authorization: Bearer <token>` | Prometheus text format |
| `POST` | `/admin/api/login` | NIP-98 from an admin | `{pubkey, expires_at, csrf}` + `__Host-ncli_admin` cookie |
| `GET` | `/admin/api/session` | admin* | who is calling (`csrf` for a cookie session) |
| `POST` | `/admin/api/logout` | admin* | ends the session |
| `GET` | `/admin/api/metrics` | admin* | latest point: `{t, v: rates/gauges/quantiles, totals}` |
| `GET` | `/admin/api/history` | admin* | last hour, columnar: `{interval_ms, t: [...], series: {key: [...]}}` |
| `GET` | `/admin/api/stream` | admin* | SSE, one `event: point` per 2s (at most 8 viewers) |
| `GET` | `/admin/api/indexes` | admin* | `{indexes: [{name, available, unavailable, estimate, running, last}]}` |
| `POST` | `/admin/api/indexes/{name}/rebuild` | admin* | body `{fresh, reverify, dry_run}`; `202` new job, `200` the running one, `409` other job, `501` unavailable |
| `POST` | `/admin/api/indexes/{name}/drop` | admin* | the finished job (`202` if still running after 30s) |
| `GET` | `/admin/api/jobs`, `/admin/api/jobs/{id}` | admin* | jobs, newest first / one job |
| `DELETE` | `/admin/api/jobs/{id}` | admin* | cancels and returns the job |
| `GET` | `/admin/ui/` | none (page only) | the embedded dashboard |

\* admin: a NIP-98 header from `nip11.pubkey` or a `nip86.admins` key (body
bound by the payload tag, each event usable once), or a dashboard session
cookie (plus `X-CSRF-Token` on anything but `GET`).

Ten failed credentials from one address within a minute are refused with
`429` for the rest of that minute. Refusals are counted in
`ncli_relay_admin_denied_total{reason}` (`host`, `token`, `nip98`,
`replay`, `session`, `csrf`, `cidr`).

## Job object

`{id, index, kind: rebuild|drop, options: {fresh, reverify}, state:
running|succeeded|failed|cancelled, scanned, indexed, total (estimate),
started_at, finished_at, error}`.

## Metric families (`ncli_relay_*`)

| Area | Series |
|---|---|
| Build | `build_info{version}`, `uptime_seconds`, Go and process collectors |
| Connections | `ws_sessions`, `ws_sessions_total`, `subscriptions_active`, `subscriptions_limit_hits_total`, `ws_send_seconds`, `ws_send_errors_total`, `ws_slow_sends_total` |
| Messages | `messages_total{type}` (EVENT, REQ, CLOSE, AUTH, COUNT, NEG, malformed) |
| Events | `events_total{result}` (accepted, duplicate, rejected, command), `events_rejected_total{reason}`, `events_by_class_total{class}`, `event_ok_seconds` |
| Reads | `req_eose_seconds`, `req_events_returned`, `count_seconds`, `reads_refused_total{reason}`, `scan_busy_total`, `scans_inflight`, `scan_slots` |
| Store | `store_batch_seconds`, `store_batch_size`, `store_batch_errors_total`, `store_task_queue_depth`, `store_task_queue_capacity`, `store_read_txs_open`, `store_size_bytes` (sampled every 30s) |
| Access | `nip42_auth_accepted_total`, `nip42_auth_rejected_total{reason}`, `admin_denied_total{reason}` |
| Workers | `verify_queue_depth`, `verify_processed_total`, `verify_errors_total`, `verify_dropped_total`, `search_index_queue_depth`, `search_index_dropped_total` |
| Features | `members`, `groups`, `huddle_rooms`, `huddle_peers`, `http_requests_total{route,code}`, `http_request_seconds{route}` |
| Jobs | `jobs_running`, `job_runs_total{index,result}` |

`reason` is the NIP-01 prefix (`invalid`, `pow`, `auth-required`,
`restricted`, `blocked`, `rate-limited`, `mute`, `error`, `busy`, `other`);
`class` is one of a fixed set (`note`, `reaction`, `zap`, `dm`, `giftwrap`,
`group`, `list`, `long-form`, `ephemeral`, `addressable`, ...).
