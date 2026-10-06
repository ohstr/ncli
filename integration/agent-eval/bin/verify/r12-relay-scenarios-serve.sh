#!/usr/bin/env bash
# Ground truth for R12. Same situation as R11 for the agent's own four
# relays -- each is stopped before the round finishes, so there's little
# durable state on those specific ports to re-check directly. The one
# piece of infrastructure that *does* outlive the round is the
# pre-started Meilisearch (prepare_r10 in bin/run.sh) -- re-query it
# directly rather than trusting the self-report's claim that the profile
# got indexed.
set -uo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source bin/lib.sh
RUN_DIR="$1"
ROUND="r12-relay-scenarios-serve"

self_report_exists "${RUN_DIR}" "${ROUND}" \
  && add_check "self_report_written" true "present" \
  || add_check "self_report_written" false "missing"

STEP_COUNT="$(self_report_field "${RUN_DIR}" "${ROUND}" '.steps | length')"
if [ -n "${STEP_COUNT}" ] && [ "${STEP_COUNT}" -ge 4 ]; then
  add_check "self_report_covers_all_four_scenarios" true "${STEP_COUNT} steps recorded"
else
  add_check "self_report_covers_all_four_scenarios" false "only ${STEP_COUNT:-0} steps recorded, want at least 4"
fi

if agent_exec 'curl -fsS http://localhost:7700/health' 2>/dev/null | grep -q '"available"'; then
  add_check "meilisearch_still_up" true "health check answered available"

  INDEXES="$(agent_exec 'curl -fsS -H "Authorization: Bearer masterKey" http://localhost:7700/indexes' 2>/dev/null)"
  if jq -e '(.results // []) | length > 0' >/dev/null 2>&1 <<<"${INDEXES}"; then
    add_check "meilisearch_has_an_index" true "$(jq -c '[.results[].uid]' <<<"${INDEXES}")"
  else
    add_check "meilisearch_has_an_index" false "no indexes present: ${INDEXES}"
  fi
else
  add_check "meilisearch_still_up" false "health check failed -- see prepare_r10's own warning above if it never came up"
fi

# Cleanup discipline, same reasoning as R11's verifier.
for port in 6511 6512 6513 6514; do
  if agent_exec "curl -fsS -o /dev/null -m 2 http://localhost:${port}/" >/dev/null 2>&1; then
    add_check "port_${port}_stopped" false "still answering after the round finished"
  else
    add_check "port_${port}_stopped" true "not listening (expected -- stopped before the round finished)"
  fi
done

write_verify "${ROUND}" "${RUN_DIR}"
