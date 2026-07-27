#!/usr/bin/env bash
# Ground truth for R2: independently re-run the same read-only queries
# against the real public relay, and confirm the relay list was left
# clean as the round instructs.
set -uo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source bin/lib.sh
RUN_DIR="$1"
ROUND="r2-query"

self_report_exists "${RUN_DIR}" "${ROUND}" \
  && add_check "self_report_written" true "present" \
  || add_check "self_report_written" false "missing"

PING="$(agent_exec 'ncli ping wss://relay.ohstr.com --json' 2>/dev/null)"
if jq -e '.reachable >= 1' >/dev/null 2>&1 <<<"${PING}"; then
  add_check "public_relay_reachable" true "ncli ping wss://relay.ohstr.com --json -> reachable"
else
  add_check "public_relay_reachable" false "ncli ping: ${PING}"
fi

FOUND="$(agent_exec 'ncli find --kinds 1 --limit 1 -s wss://relay.ohstr.com' 2>/dev/null)"
if jq -e 'type == "array" and length >= 1' >/dev/null 2>&1 <<<"${FOUND}"; then
  add_check "find_returns_real_events" true "find returned $(jq 'length' <<<"${FOUND}") kind:1 event(s)"
else
  add_check "find_returns_real_events" false "find did not return a non-empty array: ${FOUND}"
fi

RELAYS="$(agent_exec 'ncli prefs relays list --json' 2>/dev/null)"
if jq -e 'if type=="array" then length==0 else (.relays // []) | length==0 end' >/dev/null 2>&1 <<<"${RELAYS}"; then
  add_check "relay_list_left_clean" true "prefs relays list is empty, as the round instructs"
else
  add_check "relay_list_left_clean" false "prefs relays list is not empty: ${RELAYS}"
fi

write_verify "${ROUND}" "${RUN_DIR}"
