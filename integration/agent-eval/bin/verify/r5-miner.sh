#!/usr/bin/env bash
# Ground truth for R5: fetch the claimed event back from the relay and
# count its own leading-zero *bits* ourselves (NIP-13's actual definition
# of difficulty) rather than trusting `ncli miner check`'s verdict alone --
# a bug in ncli's own difficulty counter is exactly the kind of thing a
# from-scratch re-check catches that re-running the same tool wouldn't.
set -uo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source bin/lib.sh
RUN_DIR="$1"
ROUND="r5-miner"

self_report_exists "${RUN_DIR}" "${ROUND}" \
  && add_check "self_report_written" true "present" \
  || add_check "self_report_written" false "missing"

CANDIDATES="$(self_report_field "${RUN_DIR}" "${ROUND}" \
  '.. | strings | select(test("^[0-9a-f]{64}$"))' | sort -u)"

# Deliberately checks "mining actually happened at all" (>=1 leading zero
# bit) rather than pinning to the agent's own claimed target difficulty --
# reliably extracting "the" target number back out of free-form
# self-report JSON isn't worth the false-negative risk it'd add here.
#
# Self-reports mention several bare 64-hex strings (pubkeys, pub_hex,
# the actual event id, ...), often with a pubkey appearing earlier in
# the JSON than the real event id (e.g. an "identity" block up top).
# Taking just the first match is a false-negative trap -- try every
# candidate against the relay and accept the first that round-trips.
if [ -z "${CANDIDATES}" ]; then
  add_check "mined_event_meets_claimed_difficulty" false "could not find a 64-hex event id in the self-report"
else
  FOUND_ID=""
  BITS=""
  LAST_FOUND=""
  for id in ${CANDIDATES}; do
    FOUND="$(agent_exec "ncli find -i ${id} -s ws://localhost:5500" 2>/dev/null)"
    LAST_FOUND="${FOUND}"
    if jq -e --arg id "${id}" 'type=="array" and length>=1 and .[0].id == $id' >/dev/null 2>&1 <<<"${FOUND}"; then
      FOUND_ID="${id}"
      BITS="$(agent_exec "node -e \"const id='${id}'; let bits=0; for (const c of id){ const n=parseInt(c,16); if(n===0){bits+=4;continue;} bits += Math.clz32(n)-28; break;} console.log(bits)\"" 2>/dev/null | tr -dc '0-9')"
      break
    fi
  done
  if [ -z "${FOUND_ID}" ]; then
    add_check "mined_event_meets_claimed_difficulty" false "none of the self-report's 64-hex candidates (${CANDIDATES}) were found on the relay; last attempt: ${LAST_FOUND}"
  elif [ -n "${BITS}" ] && [ "${BITS}" -ge 1 ]; then
    add_check "mined_event_meets_claimed_difficulty" true "event id ${FOUND_ID} has ${BITS} leading zero bits"
  else
    add_check "mined_event_meets_claimed_difficulty" false "computed ${BITS:-0} leading zero bits for ${FOUND_ID}"
  fi
fi

write_verify "${ROUND}" "${RUN_DIR}"
