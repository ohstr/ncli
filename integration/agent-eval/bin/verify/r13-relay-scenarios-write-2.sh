#!/usr/bin/env bash
# Ground truth for R13. Same situation as R11/R12's own verifiers: every
# relay this round starts is the agent's own, on a scratch port, stopped
# before the round finishes -- so there's little durable state left to
# re-check directly once the round has exited. See r11-relay-scenarios-write.sh's
# own comment for the full reasoning; this is the same shape.
set -uo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source bin/lib.sh
RUN_DIR="$1"
ROUND="r13-relay-scenarios-write-2"

self_report_exists "${RUN_DIR}" "${ROUND}" \
  && add_check "self_report_written" true "present" \
  || add_check "self_report_written" false "missing"

STEP_COUNT="$(self_report_field "${RUN_DIR}" "${ROUND}" '.steps | length')"
if [ -n "${STEP_COUNT}" ] && [ "${STEP_COUNT}" -ge 4 ]; then
  add_check "self_report_covers_all_four_scenarios" true "${STEP_COUNT} steps recorded"
else
  add_check "self_report_covers_all_four_scenarios" false "only ${STEP_COUNT:-0} steps recorded, want at least 4"
fi

# Cleanup discipline, same reasoning as R11/R12's verifiers.
for port in 6521 6522 6523 6524; do
  if agent_exec "curl -fsS -o /dev/null -m 2 http://localhost:${port}/" >/dev/null 2>&1; then
    add_check "port_${port}_stopped" false "still answering after the round finished"
  else
    add_check "port_${port}_stopped" true "not listening (expected -- stopped before the round finished)"
  fi
done

write_verify "${ROUND}" "${RUN_DIR}"
