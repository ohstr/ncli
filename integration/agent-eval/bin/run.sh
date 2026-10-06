#!/usr/bin/env bash
# Orchestrates one full run of the ncli agent-capability eval: brings up
# the isolated stack, drives each round as a fresh, non-interactive
# `claude -p` invocation inside the agent container, runs a harness-side
# deterministic verifier and an LLM-judge pass against each round's
# transcript, and assembles one report.
#
# Usage:
#   bin/run.sh                       # every round, in order
#   bin/run.sh r0-bootstrap r1-identity   # just these, in order given
#
# Requires: docker + docker compose, and this host already logged into
# Claude Code (`claude` has worked here at least once) -- see README.md.
set -euo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

ALL_ROUNDS=(r0-bootstrap r1-identity r2-query r3-relay-ops r4-publish-apply r5-miner r6-bunker r7-blossom r8-error-contract r9-groups r10-space r11-relay-scenarios-write r12-relay-scenarios-serve r13-relay-scenarios-write-2)
ROUNDS=("${@:-${ALL_ROUNDS[@]}}")

RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)"
RUN_DIR="report/${RUN_ID}"
mkdir -p "${RUN_DIR}"
mkdir -p report
# uid 10001 == evaluser in agent/Dockerfile writes its self-reports
# straight into this bind mount. World-writable rather than chown'd: this
# directory is tracked (report/.gitkeep), so chown would need root and
# would leave the developer's own checkout owned by a foreign uid.
chmod 777 report

echo "==> run ${RUN_ID}: ${ROUNDS[*]}"

# --- credentials: copy, never bind-mount the host's real ~/.claude -------
HOST_CREDS="${HOME}/.claude/.credentials.json"
HOST_CLAUDE_JSON="${HOME}/.claude.json"
if [ ! -f "${HOST_CREDS}" ]; then
  echo "ERROR: ${HOST_CREDS} not found. Log into Claude Code on this host first (run 'claude' once) before running this suite." >&2
  exit 1
fi
rm -rf .creds-seed
mkdir -p .creds-seed
cp "${HOST_CREDS}" .creds-seed/credentials.json
[ -f "${HOST_CLAUDE_JSON}" ] && cp "${HOST_CLAUDE_JSON}" .creds-seed/claude.json || true
# uid 10001 == evaluser inside agent/Dockerfile. Owned + 700/600 so only
# that exact container-local user can read it, not "anyone on this host".
chown -R 10001:10001 .creds-seed
chmod 700 .creds-seed
chmod 600 .creds-seed/*.json

# --- a per-run vault password -- see compose.yaml's own comment ----------
# Rewritten every run, not just when missing: a leftover .env from an
# earlier run would otherwise pin the same password indefinitely.
echo "NCLI_VAULT_PASSWORD=$(head -c 24 /dev/urandom | base64 | tr -dc 'A-Za-z0-9')" > .env

cleanup() {
  echo "==> tearing down"
  docker compose down -v >/dev/null 2>&1 || true
  rm -rf .creds-seed
  # .env is deliberately left in place: compose reads it, and deleting it
  # would make a post-mortem `docker compose logs/ps/exec` here run with a
  # blank vault password. Rewriting it per run (above) is what keeps the
  # password from going stale.
}
trap cleanup EXIT

# --- NCLI_LOCAL=1: run this checkout's ncli instead of the published one --
# Built on the host so go.mod's own resolution (a local replace included)
# applies; compose.local.yaml mounts it into the relay and the agent.
if [ "${NCLI_LOCAL:-}" = "1" ]; then
  echo "==> building local ncli"
  mkdir -p .local
  (cd ../.. && GOWORK=off CGO_ENABLED=0 GOOS=linux go build -o integration/agent-eval/.local/ncli ./cmd/ncli)
  export COMPOSE_FILE=compose.yaml:compose.local.yaml
fi

echo "==> building agent image"
docker compose build agent

# `agent`'s home dir (where the vault/prefs/PATH install live) is on the
# container's own writable layer, not a named volume -- so if a prior
# run crashed or got rate-limited before reaching the cleanup trap
# above, `up -d` would otherwise silently reuse that leftover container
# instead of a fresh one, letting stale vault/install state leak into
# this run. Force a clean slate unconditionally before starting.
echo "==> ensuring a clean slate (tearing down any leftover stack from a prior run)"
docker compose down -v >/dev/null 2>&1 || true

echo "==> starting stack"
docker compose up -d

echo "==> waiting for the agent container to be responsive"
for _ in $(seq 1 30); do
  docker compose exec -T agent true >/dev/null 2>&1 && break
  sleep 1
done

# R10: a second participant (eval-peer) joins the agent's space headless
# and posts in its chat, so the agent's own join stream has someone to see.
# Long enough to still be there whenever the agent gets round to joining.
prepare_r10() {
  echo "==> [r10-space] preparing (eval-peer identity)"
  docker compose exec -T agent bash -lc '
    export PATH="$HOME/.local/bin:$PATH"
    ncli id eval-peer --json >/dev/null 2>&1 || ncli id --save --label eval-peer --json >/dev/null
  ' || echo "ERROR: [r10-space] could not create the eval-peer identity" >&2
}

run_r10() {
  run_round r10-space &
  local claude_pid=$!
  local space=""
  for _ in $(seq 1 120); do
    if [ -s "report/r10-space.txt" ]; then
      space="$(tr -d '[:space:]' < report/r10-space.txt)"
      break
    fi
    sleep 2
  done
  if [ -n "${space}" ]; then
    echo "==> [r10-space] peer joining ${space}"
    docker compose exec -T agent bash -lc "export PATH=\"\$HOME/.local/bin:\$PATH\"; ncli space join '${space}' --relay ws://localhost:5500 --identity eval-peer --duration 300s --json" \
      > "${RUN_DIR}/r10-peer.ndjson" 2> "${RUN_DIR}/r10-peer.stderr.log" &
    local peer_pid=$!
    for wait_s in 30 30 60; do
      sleep "${wait_s}"
      docker compose exec -T agent bash -lc "export PATH=\"\$HOME/.local/bin:\$PATH\"; ncli space chat send '${space}' 'hello from the peer' --relay ws://localhost:5500 --identity eval-peer --json" \
        >> "${RUN_DIR}/r10-peer-chat.json" 2>&1 || true
    done
    wait "${peer_pid}" || true
  else
    echo "WARNING: [r10-space] agent never wrote the space coordinate" >&2
  fi
  wait "${claude_pid}" || true
  cp "report/r10-space.txt" "${RUN_DIR}/r10-space.txt" 2>/dev/null || true
}

# Rounds write these into the /report mount root, and they're only copied
# into ${RUN_DIR} afterwards -- so without clearing them first, a round can
# read the previous run's file and report on stale data. Called from the
# round loop rather than run_round(), because run_r6 backgrounds run_round
# and polls for r6-bunker-uri.txt: clearing inside the background job would
# race that poll onto a stale URI.
clear_flat_artifacts() {
  local round="$1"
  rm -f "report/${round}.self-report.json"
  case "${round}" in
    r3-relay-ops) rm -f "report/r3-relay-stderr.log" ;;
    r6-bunker) rm -f "report/r6-bunker-uri.txt" ;;
    r10-space) rm -f "report/r10-space.txt" "report/r10-join.ndjson" ;;
  esac
}

run_round() {
  local round="$1"
  local prompt
  prompt="$(cat rounds/_preamble.md; echo; cat "rounds/${round}.md"; echo
    echo "## Self-report format"
    echo
    echo "Write the self-report as one JSON object with exactly these top-level keys"
    echo "(extra round-specific keys are fine alongside them):"
    echo
    cat rounds/_report-schema.json)"
  echo "==> [${round}] running agent"
  docker compose exec -T agent claude -p "${prompt}" \
    --output-format stream-json --verbose \
    --permission-mode bypassPermissions \
    > "${RUN_DIR}/${round}.transcript.jsonl" 2> "${RUN_DIR}/${round}.stderr.log"
  local status=$?
  cp "report/${round}.self-report.json" "${RUN_DIR}/${round}.self-report.json" 2>/dev/null \
    || echo "WARNING: [${round}] agent did not write a self-report" >&2
  return ${status}
}

# R2 queries the stack's own relay, which starts empty -- seed it with a
# handful of kind:1 events so the round has something real to ping, find
# and dump. Keeps the round hermetic: no public relay involved.
prepare_r2() {
  echo "==> [r2-query] seeding the stack's relay"
  # Only stdout is silenced, so a failing step's own stderr reaches the
  # run log instead of being swallowed.
  if ! docker compose exec -T agent bash -lc '
    set -e
    export PATH="$HOME/.local/bin:$PATH"
    # The .json suffix is load-bearing: ncli rejects an --events path whose
    # extension is not one of .json/.jsonp/.yaml/.yml, and a bare mktemp
    # name has none.
    unsigned=$(mktemp --suffix=.json) && signed=$(mktemp --suffix=.json)
    trap "rm -f \"$unsigned\" \"$signed\"" EXIT
    ncli id eval-seed --json >/dev/null 2>&1 || ncli id --save --label eval-seed --json >/dev/null
    jq -nc "[range(0;8) | {kind:1, content:(\"ncli eval seed \" + (.|tostring)), created_at:((now|floor) - .), tags:[]}]" > "$unsigned"
    ncli id sign -e "$unsigned" -o "$signed" --identity eval-seed >/dev/null
    ncli publish -e "$signed" -s ws://localhost:5500 >/dev/null
  '; then
    echo "ERROR: [r2-query] could not seed the relay -- R2 will have nothing to query (ncli is installed by r0-bootstrap; running this round on its own skips that)" >&2
  fi
}

# R12 needs a real Meilisearch reachable at localhost:7700 before the
# round starts, matching public-search-relay.yaml's own doc literally (a
# co-located instance, not a network trick). The binary is baked into the
# agent image (see agent/Dockerfile); this just starts it detached and
# waits for its health check, the same shape as prepare_r6's daemon
# pre-start below. A failure here is a warning, not a hard stop -- the
# round still runs and will simply find the scenario's search half
# unreachable, which is itself a legitimate (if degraded) finding.
prepare_r12() {
  echo "==> [r12-relay-scenarios-serve] pre-starting Meilisearch"
  docker compose exec -T agent bash -lc '
    nohup meilisearch --http-addr 0.0.0.0:7700 --master-key masterKey --no-analytics \
      >/home/evaluser/work/.r12-meilisearch.log 2>&1 &
    disown
  ' || true
  for _ in $(seq 1 15); do
    if docker compose exec -T agent bash -lc 'curl -fsS http://localhost:7700/health' 2>/dev/null \
        | grep -q '"available"'; then
      return 0
    fi
    sleep 1
  done
  echo "WARNING: [r12-relay-scenarios-serve] meilisearch did not come up before the round started" >&2
  docker compose exec -T agent bash -lc \
    'tail -5 /home/evaluser/work/.r12-meilisearch.log 2>/dev/null' >&2 || true
}

# R6: the agent starts the bunker itself (no TTY). It needs the eval-agent
# identity R1 saved, and no daemon left over from an earlier run. Its NIP-46
# counterparty fixture runs from outside the round against whatever pairing
# URI the round produces, while the round waits on it.
prepare_r6() {
  echo "==> [r6-bunker] preparing (eval-agent identity, no running daemon)"
  if ! docker compose exec -T agent bash -lc '
    set -e
    export PATH="$HOME/.local/bin:$PATH"
    ncli id eval-agent --json >/dev/null 2>&1 || ncli id --save --label eval-agent --json >/dev/null
    ncli bunker stop --json >/dev/null 2>&1 || true
  '; then
    echo "ERROR: [r6-bunker] could not create the eval-agent identity" >&2
  fi
}

run_r6() {
  run_round r6-bunker &
  local claude_pid=$!
  local paired=0
  for _ in $(seq 1 120); do
    if [ -s "report/r6-bunker-uri.txt" ]; then
      local uri
      uri="$(cat "report/r6-bunker-uri.txt")"
      echo "==> [r6-bunker] running NIP-46 counterparty fixture against ${uri}"
      docker compose exec -T agent node /fixtures/nip46-client.cjs "${uri}" \
        > "${RUN_DIR}/r6-bunker.fixture.json" 2>&1 || true
      paired=1
      break
    fi
    sleep 2
  done
  [ "${paired}" -eq 1 ] || echo "WARNING: [r6-bunker] agent never wrote a pairing URI" >&2
  wait "${claude_pid}" || true
  cp "report/r6-bunker-uri.txt" "${RUN_DIR}/r6-bunker-uri.txt" 2>/dev/null || true
}

for round in "${ROUNDS[@]}"; do
  clear_flat_artifacts "${round}"
  if [ "${NCLI_LOCAL:-}" = "1" ]; then
    # Over whatever the agent installed (R0 installs the published one).
    docker compose exec -T agent bash -lc 'mkdir -p ~/.local/bin && install -m755 /opt/ncli-local/ncli ~/.local/bin/ncli'
  fi
  case "${round}" in
    r2-query)
      prepare_r2
      run_round "${round}" || echo "WARNING: [${round}] claude invocation exited non-zero" >&2
      ;;
    r6-bunker)
      prepare_r6
      run_r6
      ;;
    r10-space)
      prepare_r10
      run_r10
      ;;
    r12-relay-scenarios-serve)
      prepare_r12
      run_round "${round}" || echo "WARNING: [${round}] claude invocation exited non-zero" >&2
      ;;
    *)
      run_round "${round}" || echo "WARNING: [${round}] claude invocation exited non-zero" >&2
      ;;
  esac
  bin/verify/"${round}.sh" "${RUN_DIR}" || echo "WARNING: [${round}] verifier reported problems" >&2
done

echo "==> judging transcripts"
bin/judge.sh "${RUN_DIR}" "${ROUNDS[@]}"

echo "==> assembling report"
bin/report.sh "${RUN_DIR}" "${ROUNDS[@]}"

echo "==> command coverage"
coverage_ok=1
bin/coverage.sh "${RUN_DIR}" || coverage_ok=0
cat "${RUN_DIR}/coverage.md" >> "${RUN_DIR}/report.md" 2>/dev/null || true

echo "==> done: ${RUN_DIR}/report.md"
# Only a full run can be expected to cover every command.
if [ "${coverage_ok}" -eq 0 ] && [ "${ROUNDS[*]}" = "${ALL_ROUNDS[*]}" ]; then
  echo "FAIL: some commands were never run by an agent (see coverage.md)" >&2
  exit 1
fi
