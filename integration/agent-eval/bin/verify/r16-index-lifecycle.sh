#!/usr/bin/env bash
# Ground truth for R16: the relay's own job history, written next to its
# store, says what it did to the stats index -- not the agent's account.
set -uo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source bin/lib.sh
RUN_DIR="$1"
ROUND="r16-index-lifecycle"

self_report_exists "${RUN_DIR}" "${ROUND}" \
  && add_check "self_report_written" true "present" \
  || add_check "self_report_written" false "missing"

JOBS="$(agent_exec 'cat /home/evaluser/work/r16/notes.db.jobs.json' 2>/dev/null)"
has_job() { jq -e "[.[] | select(.index == \"stats\" and $1)] | length > 0" >/dev/null 2>&1 <<<"${JOBS}"; }

if has_job '.kind == "catch_up" and .trigger == "reconcile" and .state == "succeeded"'; then
  add_check "relay_caught_up_on_its_own" true "$(jq -c '[.[] | select(.kind == "catch_up")][0].summary' <<<"${JOBS}")"
else
  add_check "relay_caught_up_on_its_own" false "stats jobs: $(jq -c '[.[] | select(.index == "stats") | {kind, trigger, state}]' <<<"${JOBS}" 2>/dev/null || echo "${JOBS}")"
fi
if has_job '.kind == "rebuild" and .trigger == "admin" and .state == "succeeded"'; then
  add_check "admin_rebuild_succeeded" true "present"
else
  add_check "admin_rebuild_succeeded" false "no succeeded admin rebuild of stats"
fi
if has_job '.kind == "drop" and .trigger == "admin" and .state == "succeeded" and .entries_before > 0'; then
  add_check "drop_of_the_disabled_index_removed_its_data" true "present"
else
  add_check "drop_of_the_disabled_index_removed_its_data" false "no succeeded drop with entries"
fi
if has_job '.kind == "rebuild" and .trigger == "reconcile"'; then
  add_check "no_rebuild_where_a_catch_up_does" false "the relay rebuilt stats instead of catching up"
else
  add_check "no_rebuild_where_a_catch_up_does" true "no reconcile rebuild"
fi

# The agent read the outcome from JSON: its report names the codes ncli gave.
REPORT="$(cat "${RUN_DIR}/${ROUND}.self-report.json" 2>/dev/null)"
if grep -q 'invalid_input' <<<"${REPORT}" && grep -q '"noop"\|noop' <<<"${REPORT}"; then
  add_check "report_quotes_refusal_and_noop" true "invalid_input and noop reported"
else
  add_check "report_quotes_refusal_and_noop" false "self-report lacks invalid_input or noop"
fi

if agent_exec 'ss -ltn 2>/dev/null | grep -q ":6600 "' >/dev/null 2>&1; then
  add_check "relay_stopped" false "something still listens on 6600"
else
  add_check "relay_stopped" true "port 6600 free"
fi

write_verify "${ROUND}" "${RUN_DIR}"
