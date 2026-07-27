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
if [ ! -f .env ]; then
  echo "NCLI_VAULT_PASSWORD=$(head -c 24 /dev/urandom | base64 | tr -dc 'A-Za-z0-9')" > .env
fi

cleanup() {
  echo "==> tearing down"
  docker compose down -v >/dev/null 2>&1 || true
  rm -rf .creds-seed
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

# R6 needs its bunker daemon pre-started outside the round (starting it
# needs a real TTY -- see rounds/r6-bunker.md) and its NIP-46 counterparty
# fixture run against whatever pairing URI the round produces, from
# outside the round's own session, while it's still polling for the
# pairing to land.
prepare_r6() {
  echo "==> [r6-bunker] pre-starting bunker daemon"
  docker compose exec -T agent bash -lc '
    set -e
    export PATH="$HOME/.local/bin:$PATH"
    ncli id eval-agent --json >/dev/null 2>&1 || ncli id --save --label eval-agent --json >/dev/null
    script -qec "ncli bunker --identity eval-agent --relay ws://localhost:5500" /home/evaluser/work/.r6-daemon-tty.log >/dev/null 2>&1 || true
  '
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
  rm -f "report/r6-bunker-uri.txt"
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
  case "${round}" in
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
