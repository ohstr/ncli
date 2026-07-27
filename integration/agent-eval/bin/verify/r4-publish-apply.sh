#!/usr/bin/env bash
# Ground truth for R4: take the event ID the agent *claims* it published,
# and independently ask the relay whether that event actually exists.
set -uo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source bin/lib.sh
RUN_DIR="$1"
ROUND="r4-publish-apply"

self_report_exists "${RUN_DIR}" "${ROUND}" \
  && add_check "self_report_written" true "present" \
  || add_check "self_report_written" false "missing"

CANDIDATES="$(self_report_field "${RUN_DIR}" "${ROUND}" \
  '.. | strings | select(test("^[0-9a-f]{64}$"))' | sort -u)"

# Self-reports mention several bare 64-hex strings (pubkeys, pub_hex,
# the actual event id, ...), often with a pubkey appearing earlier in
# the JSON than the real event id (e.g. an "identity" block up top).
# Taking just the first match is a false-negative trap -- try every
# candidate against the relay and accept if any round-trips as an id.
FOUND_ID=""
LAST_RESULT=""
for id in ${CANDIDATES}; do
  RESULT="$(agent_exec "ncli find -i ${id} -s ws://localhost:5500" 2>/dev/null)"
  LAST_RESULT="${RESULT}"
  if jq -e --arg id "${id}" 'type=="array" and length>=1 and .[0].id == $id' >/dev/null 2>&1 <<<"${RESULT}"; then
    FOUND_ID="${id}"
    break
  fi
done

if [ -z "${CANDIDATES}" ]; then
  add_check "published_event_findable_on_relay" false "could not find a 64-hex event id anywhere in the self-report"
elif [ -n "${FOUND_ID}" ]; then
  add_check "published_event_findable_on_relay" true "event ${FOUND_ID} round-trips through find"
else
  add_check "published_event_findable_on_relay" false "none of the self-report's 64-hex candidates (${CANDIDATES}) round-tripped through find; last attempt: ${LAST_RESULT}"
fi

write_verify "${ROUND}" "${RUN_DIR}"
