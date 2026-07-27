#!/usr/bin/env bash
# Ground truth for R6: did the NIP-46 counterparty fixture (run by
# bin/run.sh itself, not the agent) actually manage to pair and get a
# real signed event back through ncli's bunker daemon -- the strongest
# possible signal here, since it's the harness's own independent client
# talking to the daemon, not anything the agent said about it.
set -uo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source bin/lib.sh
RUN_DIR="$1"
ROUND="r6-bunker"

self_report_exists "${RUN_DIR}" "${ROUND}" \
  && add_check "self_report_written" true "present" \
  || add_check "self_report_written" false "missing"

if [ -s "${RUN_DIR}/r6-bunker-uri.txt" ]; then
  add_check "pairing_uri_produced" true "$(cat "${RUN_DIR}/r6-bunker-uri.txt")"
else
  add_check "pairing_uri_produced" false "no r6-bunker-uri.txt was captured"
fi

FIXTURE="$(cat "${RUN_DIR}/r6-bunker.fixture.json" 2>/dev/null)"
if jq -e '.ok == true and (.signed_event.sig | length) == 128' >/dev/null 2>&1 <<<"${FIXTURE}"; then
  add_check "nip46_pairing_and_signing_actually_worked" true "fixture paired and received a real signed event back"
else
  add_check "nip46_pairing_and_signing_actually_worked" false "fixture output: ${FIXTURE}"
fi

SESSIONS="$(agent_exec 'ncli bunker sessions list --json' 2>/dev/null)"
if jq -e 'type=="array" and length>=1' >/dev/null 2>&1 <<<"${SESSIONS}"; then
  add_check "bunker_sees_the_paired_session" true "$(jq -c '. ' <<<"${SESSIONS}")"
else
  add_check "bunker_sees_the_paired_session" false "ncli bunker sessions list --json: ${SESSIONS}"
fi

write_verify "${ROUND}" "${RUN_DIR}"
