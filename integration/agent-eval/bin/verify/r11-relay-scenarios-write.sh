#!/usr/bin/env bash
# Ground truth for R11: every relay this round starts is the agent's own,
# on a scratch port, stopped before the round finishes -- same situation
# as R3 Part A, and for the same reason there's little durable state left
# to re-check directly once the round has exited. This verifier checks
# what *is* still checkable (the self-report's shape, and -- opportunistically
# -- whether a scenario's port is still live, which would mean either the
# agent forgot to stop it or it's still mid-step) and leaves judging
# whether each scenario's accept/reject outcome actually matched what was
# asked for to bin/judge.sh's transcript read, the same split R3 uses.
set -uo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source bin/lib.sh
RUN_DIR="$1"
ROUND="r11-relay-scenarios-write"

self_report_exists "${RUN_DIR}" "${ROUND}" \
  && add_check "self_report_written" true "present" \
  || add_check "self_report_written" false "missing"

STEP_COUNT="$(self_report_field "${RUN_DIR}" "${ROUND}" '.steps | length')"
if [ -n "${STEP_COUNT}" ] && [ "${STEP_COUNT}" -ge 4 ]; then
  add_check "self_report_covers_all_four_scenarios" true "${STEP_COUNT} steps recorded"
else
  add_check "self_report_covers_all_four_scenarios" false "only ${STEP_COUNT:-0} steps recorded, want at least 4"
fi

# Cleanup discipline: none of 6501-6504 should still be listening. A port
# still live either means the agent didn't stop it as asked, or (less
# likely, since the round instructs stop-before-moving-on) it's still
# mid-scenario when the round exited -- either way worth flagging, not
# silently ignoring.
for port in 6501 6502 6503 6504; do
  if agent_exec "curl -fsS -o /dev/null -m 2 http://localhost:${port}/" >/dev/null 2>&1; then
    add_check "port_${port}_stopped" false "still answering after the round finished"
  else
    add_check "port_${port}_stopped" true "not listening (expected -- stopped before the round finished)"
  fi
done

write_verify "${ROUND}" "${RUN_DIR}"
