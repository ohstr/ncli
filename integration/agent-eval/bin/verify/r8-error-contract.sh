#!/usr/bin/env bash
# Ground truth for R8: this round is *about* checking a documented
# contract, so the verifier both runs its own independent probes (the
# floor -- these must hold regardless of what the agent found) and
# sanity-checks that the agent's self-reported exit codes for the same
# well-known cases actually match AGENTS.md's table, rather than trusting
# its narration that they did.
set -uo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source bin/lib.sh
RUN_DIR="$1"
ROUND="r8-error-contract"

self_report_exists "${RUN_DIR}" "${ROUND}" \
  && add_check "self_report_written" true "present" \
  || add_check "self_report_written" false "missing"

# --- independent probe 1: usage (exit 2) ---
agent_exec 'ncli decode --json' >/tmp/r8-out.json 2>/tmp/r8-err.json
CODE=$?
ERR="$(cat /tmp/r8-err.json)"
OUT="$(cat /tmp/r8-out.json)"
if [ "${CODE}" = "2" ] && [ -z "${OUT}" ] && jq -e '.code == "usage"' >/dev/null 2>&1 <<<"${ERR}"; then
  add_check "usage_error_matches_table" true "missing arg -> exit 2, code=usage, empty stdout"
else
  add_check "usage_error_matches_table" false "exit=${CODE} stdout='${OUT}' stderr='${ERR}'"
fi

# --- independent probe 2: invalid_input (exit 3) ---
set +e
agent_exec 'ncli decode not-a-real-bech32-string --json' >/tmp/r8-out.json 2>/tmp/r8-err.json
CODE=$?
ERR="$(cat /tmp/r8-err.json)"
OUT="$(cat /tmp/r8-out.json)"
if [ "${CODE}" = "3" ] && [ -z "${OUT}" ] && jq -e '.code == "invalid_input"' >/dev/null 2>&1 <<<"${ERR}"; then
  add_check "invalid_input_error_matches_table" true "malformed bech32 -> exit 3, code=invalid_input, empty stdout"
else
  add_check "invalid_input_error_matches_table" false "exit=${CODE} stdout='${OUT}' stderr='${ERR}'"
fi

# --- independent probe 3: network (exit 6, retryable) -- via `find`,
# NOT `ping`: AGENTS.md documents ping's own unreachable-target failure
# as a deliberate `internal`/exit-1 exception (checked separately below).
agent_exec 'ncli find --kinds 1 -s ws://nonexistent.invalid --json' >/tmp/r8-out.json 2>/tmp/r8-err.json
CODE=$?
ERR="$(cat /tmp/r8-err.json)"
OUT="$(cat /tmp/r8-out.json)"
if [ "${CODE}" = "6" ] && [ -z "${OUT}" ] && jq -e '.code == "network" and .retryable == true' >/dev/null 2>&1 <<<"${ERR}"; then
  add_check "network_error_matches_table" true "find, every target unreachable -> exit 6, code=network, retryable=true"
else
  add_check "network_error_matches_table" false "exit=${CODE} stdout='${OUT}' stderr='${ERR}'"
fi

# --- independent probe 3b: ping's documented exception (internal, exit 1) ---
agent_exec 'ncli ping ws://nonexistent.invalid --json' >/tmp/r8-out.json 2>/tmp/r8-err.json
CODE=$?
ERR="$(cat /tmp/r8-err.json)"
if [ "${CODE}" = "1" ] && jq -e '.code == "internal"' >/dev/null 2>&1 <<<"${ERR}"; then
  add_check "ping_unreachable_exception_matches_table" true "ping, unreachable target -> exit 1, code=internal (AGENTS.md's documented exception)"
else
  add_check "ping_unreachable_exception_matches_table" false "exit=${CODE} stderr='${ERR}' (AGENTS.md says this should be internal/1, not network)"
fi

# --- cross-check the agent's own reported probes against the same table ---
BAD_CLAIMS="$(jq -c '
  [ (.steps // [])[]
    | select(.exit_code != null)
    | select(
        ((.result // "" ) | test("usage"; "i")) and (.exit_code != 2) or
        ((.result // "" ) | test("invalid_input"; "i")) and (.exit_code != 3) or
        ((.result // "" ) | test("not_found"; "i")) and (.exit_code != 4) or
        ((.result // "" ) | test("\"network\""; "i")) and (.exit_code != 6)
      )
  ]
' "${RUN_DIR}/${ROUND}.self-report.json" 2>/dev/null || echo '[]')"
if [ "${BAD_CLAIMS}" = "[]" ] || [ -z "${BAD_CLAIMS}" ]; then
  add_check "agents_own_probes_self_consistent" true "no self-reported (code, exit_code) mismatch found"
else
  add_check "agents_own_probes_self_consistent" false "self-report has code/exit_code mismatches: ${BAD_CLAIMS}"
fi

write_verify "${ROUND}" "${RUN_DIR}"
