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
    go test -tags integration ./cli/bunker/... -run Live -v -count=1

# Run the stream integration test (needs Docker; brings up/tears down its
# own local relay containers -- see integration/stream/README.md -- and hits
# no real production relay). Runs automatically in CI (see
# .github/workflows/ci.yml's `integrations` job); not part of
# `just test`/`test-integration`.
test-integration-stream:
    go test ./client/... -run 'TestStreamIntegration' -v -count=1 -timeout 10m

# Run the inspect integration test (needs Docker; see
# integration/inspect/README.md). Runs automatically in CI; not part of
# `just test`/`test-integration`.
test-integration-inspect:
    go test ./client/... -run 'TestInspectIntegration' -v -count=1 -timeout 10m

# Run the sync integration test (needs Docker; see
# integration/sync/README.md). Runs automatically in CI; not part of
# `just test`/`test-integration`.
test-integration-sync:
    go test ./client/... -run 'TestSyncIntegration' -v -count=1 -timeout 10m

# Run every hermetic integration test together (stream + inspect + sync --
# needs Docker). This is what .github/workflows/ci.yml's `integrations` job
# actually runs, so `just test-integrations` reproduces a CI failure
# locally exactly. Each Test<Feature>Integration brings up/tears down its
# own compose stack (see each integration/<feature>/README.md), so running
# them together here is just one `go test` invocation, not a shared stack.
test-integrations:
    go test ./client/... -run 'TestStreamIntegration|TestInspectIntegration|TestSyncIntegration' -v -count=1 -timeout 30m

# Run the client package's benchmarks (stream pipeline hot paths)
bench:
    go test ./client/... -run '^$' -bench . -benchmem

# Regenerate the README's demo GIFs from docs/vhs/*.tape (needs vhs, ttyd,
# and ffmpeg on PATH -- https://github.com/charmbracelet/vhs). Hits a real
# public relay (wss://relay.primal.net) for live data, so results aren't
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

# Local stream e2e test stack (real destination + source ncli relay
# containers -- see integration/stream/README.md): [up|down]. The Go test
# behind `just test-integration-stream` manages its own compose lifecycle,
# so this is for poking at the stack by hand (e.g. running a real
# `ncli apply -f integration/stream/stream.yaml` against it).
stream cmd="up" *args:
    #!/usr/bin/env bash
    set -euo pipefail
    case "{{cmd}}" in
    "up") docker compose -f integration/stream/compose.yaml up -d --build {{args}} ;;
    "down") docker compose -f integration/stream/compose.yaml down -v {{args}} ;;
    *) echo "unknown stream subcommand: {{cmd}} (expected up|down)" >&2 && exit 1 ;;
    esac

# Local inspect e2e test stack (three real ncli relay containers -- see
# integration/inspect/README.md): [up|down]. The Go test behind
# `just test-integration-inspect` manages its own compose lifecycle, so
# this is for poking at the stack by hand.
inspect cmd="up" *args:
    #!/usr/bin/env bash
    set -euo pipefail
    case "{{cmd}}" in
    "up") docker compose -f integration/inspect/compose.yaml up -d --build {{args}} ;;
    "down") docker compose -f integration/inspect/compose.yaml down -v {{args}} ;;
    *) echo "unknown inspect subcommand: {{cmd}} (expected up|down)" >&2 && exit 1 ;;
    esac

# Local sync e2e test stack (one real ncli relay container -- see
# integration/sync/README.md): [up|down]. The Go test behind
# `just test-integration-sync` manages its own compose lifecycle, so this
# is for poking at the stack by hand.
sync cmd="up" *args:
    #!/usr/bin/env bash
    set -euo pipefail
    case "{{cmd}}" in
    "up") docker compose -f integration/sync/compose.yaml up -d --build {{args}} ;;
    "down") docker compose -f integration/sync/compose.yaml down -v {{args}} ;;
    *) echo "unknown sync subcommand: {{cmd}} (expected up|down)" >&2 && exit 1 ;;
    esac

# Run the docs site locally with hot reload -- README.md, AGENTS.md, and
# CHANGELOG.md changes sync automatically. http://localhost:4321/
#
# The docs site app itself lives in ohstr/docs-kit, shared across ohstr
# projects (see that repo's README) rather than vendored here -- this just
# clones/updates a local cache of it under .docs-kit/ (gitignored) and runs
# it against this repo's own content.
docs-dev:
    [ -d .docs-kit/.git ] && git -C .docs-kit pull --quiet || git clone --quiet https://github.com/ohstr/docs-kit .docs-kit
    cd .docs-kit && [ -d node_modules ] || npm install
    cd .docs-kit && DOCS_CONTENT_DIR="{{justfile_directory()}}" DOCS_TITLE=ncli DOCS_ACCENT_HUE=262 DOCS_FAVICON_GLYPH='>_' npm run dev
