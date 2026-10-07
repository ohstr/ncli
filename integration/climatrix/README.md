# climatrix

Black-box test matrix for every `ncli` command. Builds the binary from
source, starts local `ncli relay` processes on demand, drives commands the
way a script or a person would. No Docker, no public relays.

```sh
just test-integration-cli        # everything
go test -short ./integration/climatrix/   # fast subset (also in `just check`)
```

- **Relays**: `StartRelay(t, baseYAML, overrides)` on ports 21600-21639;
  port, store, logs and `nip11.url` are always set by the harness.
- **Data**: `Relay.Seed` publishes `testdata/events.json` (339 events).
- **Actors**: fixed keys in `testdata/actors.json`, via `A(t, "alice")`.
- **nip-05**: `StartNip05` serves `.well-known/nostr.json` over local TLS;
  identifiers look like `alice@127.0.0.1:<port>`.
- **Isolation**: each `NewEnv` has its own config dir and no inherited
  `NCLI_*` variables.
- **No public relays**: `TestNoPublicRelayInTests` fails on any test file
  naming one, unless listed as parse-only with a reason.

Inside a parent `go.work` that maps this module elsewhere, run with
`GOWORK=off`.
