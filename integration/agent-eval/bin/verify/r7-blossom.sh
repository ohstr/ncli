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

# Step 8: a kind:10063 by eval-agent naming the server is on the relay.
PUB="$(agent_exec 'ncli id eval-agent --json | jq -r .pub_hex' 2>/dev/null)"
LISTS="$(agent_exec "ncli find --authors ${PUB} --kinds 10063 -s ws://localhost:5500" 2>/dev/null)"
if jq -e 'any(.[]; .kind == 10063 and any(.tags[]; .[0] == "server" and (.[1] | startswith("http://blossom:3000"))))' >/dev/null 2>&1 <<<"${LISTS}"; then
  add_check "server_list_published" true "kind:10063 by ${PUB} names http://blossom:3000"
else
  add_check "server_list_published" false "find kind:10063: ${LISTS}"
fi

# Step 9: the default list ends empty.
SERVERS="$(agent_exec 'ncli blossom servers list --json' 2>/dev/null)"
if jq -e '.servers | type == "array" and length == 0' >/dev/null 2>&1 <<<"${SERVERS}"; then
  add_check "server_removed" true "default server list is []"
else
  add_check "server_removed" false "servers list: ${SERVERS}"
fi

write_verify "${ROUND}" "${RUN_DIR}"
