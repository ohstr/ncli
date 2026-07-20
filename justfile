set shell := ["bash", "-uc"]

# List all available recipes
default:
    @just --list --unsorted

# Build the ncli binary into ./bin/ncli
build:
    go build -o bin/ncli ./cmd/ncli

# Run the test suite (skips live-relay integration tests; see test-integration)
test:
    go test -short -race ./...

# Run the live-relay integration tests (hits real public Nostr relays; not run in CI)
test-integration:
    go test ./client/... -run 'TestMultiRelaySync|TestNegSync_Integration' -v -count=1

# Run the client package's benchmarks (stream pipeline hot paths)
bench:
    go test ./client/... -run '^$' -bench . -benchmem

# Regenerate the README's demo GIFs from docs/vhs/*.tape (needs vhs, ttyd,
# and ffmpeg on PATH -- https://github.com/charmbracelet/vhs). Hits a real
# public relay (wss://relay.damus.io) for live data, so results aren't
# byte-for-byte reproducible between runs. Pass a name (e.g. `just vhs apply`)
# to only regenerate docs/vhs/<name>.tape.
vhs name="*": build
    #!/usr/bin/env bash
    set -euo pipefail
    export PATH="$PWD/bin:$PATH"
    # Chrome refuses to launch sandboxed as root (e.g. in containers/CI).
    if [ "$(id -u)" -eq 0 ]; then
        export VHS_NO_SANDBOX=1
    fi
    shopt -s nullglob
    tapes=(docs/vhs/{{name}}.tape)
    if [ ${#tapes[@]} -eq 0 ]; then
        echo "no tapes matching docs/vhs/{{name}}.tape" >&2
        exit 1
    fi
    for tape in "${tapes[@]}"; do
        echo "==> $tape"
        vhs "$tape"
    done
    echo "Note: docs/vhs/apply-stream.tape writes into the tracked data/relay1 and" >&2
    echo "data/relay2 stores (examples/apply/stream.yaml's own destinations)." >&2
    echo "Run 'git checkout -- data/relay1 data/relay2' before committing" >&2
    echo "unless you actually want to check in the freshly streamed data." >&2

# Vet all packages
vet:
    go vet ./...

# Tidy go.mod / go.sum
tidy:
    go mod tidy

# Run vet + test together (local pre-push check)
check: vet test

# Local dev stack: [relay|up|down]
dev cmd="relay" *args:
    #!/usr/bin/env bash
    set -euo pipefail
    case "{{cmd}}" in
    "relay") just _dev-relay {{args}} ;;
    "up") just _dev-up {{args}} ;;
    "down") just _dev-down {{args}} ;;
    *) echo "unknown dev subcommand: {{cmd}} (expected relay|up|down)" >&2 && exit 1 ;;
    esac

# Run the relay server against the example config
_dev-relay:
    go run ./cmd/ncli relay --config ./examples/relay/minimal.yaml

# Start the local dev stack (relay + Meilisearch) via Docker Compose
_dev-up:
    docker compose -f build/relay/docker-compose.dev.yaml up

# Stop the local dev stack
_dev-down:
    docker compose -f build/relay/docker-compose.dev.yaml down
