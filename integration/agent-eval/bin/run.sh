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

ALL_ROUNDS=(r0-bootstrap r1-identity r2-query r3-relay-ops r4-publish-apply r5-miner r6-bunker r7-blossom r8-error-contract)
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
    r6-bunker) rm -f "report/r6-bunker-uri.txt" ;;
  esac
}

run_round() {
  local round="$1"
  local prompt
  prompt="$(cat rounds/_preamble.md; echo; cat "rounds/${round}.md")"
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
    unsigned=$(mktemp) && signed=$(mktemp)
    trap "rm -f \"$unsigned\" \"$signed\"" EXIT
    ncli id eval-seed --json >/dev/null 2>&1 || ncli id --save --label eval-seed --json >/dev/null
    jq -nc "[range(0;8) | {kind:1, content:(\"ncli eval seed \" + (.|tostring)), created_at:((now|floor) - .), tags:[]}]" > "$unsigned"
    ncli id sign -e "$unsigned" -o "$signed" --identity eval-seed >/dev/null
    ncli publish -e "$signed" -s ws://localhost:5500 >/dev/null
  '; then
    echo "ERROR: [r2-query] could not seed the relay -- R2 will have nothing to query (ncli is installed by r0-bootstrap; running this round on its own skips that)" >&2
  fi
}

# R6 needs its bunker daemon pre-started outside the round (starting it
# needs a real TTY -- see rounds/r6-bunker.md) and its NIP-46 counterparty
# fixture run against whatever pairing URI the round produces, from
# outside the round's own session, while it's still polling for the
# pairing to land.
prepare_r6() {
  echo "==> [r6-bunker] pre-starting bunker daemon"
  # The identity step's failure used to be invisible -- most often a wrong
  # vault password -- surfacing only 15s later as the generic "daemon did
  # not come up" warning. Check it and name the cause. Not captured via
  # $(...): this spawns a detached daemon, and command substitution would
  # block until every writer to the pipe closed.
  if ! docker compose exec -T agent bash -lc '
    set -e
    export PATH="$HOME/.local/bin:$PATH"
    ncli id eval-agent --json >/dev/null 2>&1 || ncli id --save --label eval-agent --json >/dev/null
    script -qec "ncli bunker --identity eval-agent --relay ws://localhost:5500" /home/evaluser/work/.r6-daemon-tty.log >/dev/null 2>&1 || true
  '; then
    echo "ERROR: [r6-bunker] could not create the eval-agent identity or start the daemon" >&2
  fi
  for _ in $(seq 1 15); do
    if docker compose exec -T agent bash -lc 'export PATH="$HOME/.local/bin:$PATH"; ncli bunker status --json' 2>/dev/null \
        | grep -q '"running": *true'; then
      return 0
    fi
    sleep 1
  done
  echo "WARNING: [r6-bunker] bunker daemon did not come up before the round started" >&2
}

run_r6() {
  run_round r6-bunker &
  local claude_pid=$!
  local paired=0
  for _ in $(seq 1 40); do
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
  case "${round}" in
    r2-query)
      prepare_r2
      run_round "${round}" || echo "WARNING: [${round}] claude invocation exited non-zero" >&2
      ;;
    r6-bunker)
      prepare_r6
      run_r6
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

echo "==> done: ${RUN_DIR}/report.md"
