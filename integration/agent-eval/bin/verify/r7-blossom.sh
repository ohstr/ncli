#!/usr/bin/env bash
# Ground truth for R7: hit the Blossom server directly for the claimed
# hash. The round has the agent delete the blob at the end, so the
# correct ground truth by the time this runs is "gone" (404) -- if it's
# still there, either the delete silently failed or never ran.
set -uo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source bin/lib.sh
RUN_DIR="$1"
ROUND="r7-blossom"

self_report_exists "${RUN_DIR}" "${ROUND}" \
  && add_check "self_report_written" true "present" \
  || add_check "self_report_written" false "missing"

SHA="$(self_report_field "${RUN_DIR}" "${ROUND}" \
  '.. | strings | select(test("^[0-9a-f]{64}$"))' | head -1)"

if [ -z "${SHA}" ]; then
  add_check "blob_actually_deleted" false "could not find a sha256 hash in the self-report"
else
  CODE="$(agent_exec "curl -s -o /dev/null -w '%{http_code}' http://blossom:3000/${SHA}" 2>/dev/null)"
  if [ "${CODE}" = "404" ]; then
    add_check "blob_actually_deleted" true "GET http://blossom:3000/${SHA} -> 404, as expected after rm"
  else
    add_check "blob_actually_deleted" false "GET http://blossom:3000/${SHA} -> ${CODE} (expected 404)"
  fi
fi

write_verify "${ROUND}" "${RUN_DIR}"
