#!/usr/bin/env bash
# Ground truth for R10: the agent's own join stream (it must be valid
# NDJSON showing the harness's peer), and the space's chat on the relay.
set -uo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source bin/lib.sh
RUN_DIR="$1"
ROUND="r10-space"
RELAY="ws://localhost:5500"

self_report_exists "${RUN_DIR}" "${ROUND}" \
  && add_check "self_report_written" true "present" \
  || add_check "self_report_written" false "missing"

ADMIN="$(agent_exec 'ncli id eval-agent --json | jq -r .pub_hex' 2>/dev/null)"
PEER="$(agent_exec 'ncli id eval-peer --json | jq -r .pub_hex' 2>/dev/null)"
SPACE="30312:${ADMIN}:r10-standup"

if [ "$(tr -d '[:space:]' < report/r10-space.txt 2>/dev/null)" = "${SPACE}" ]; then
  add_check "space_created" true "${SPACE}"
else
  add_check "space_created" false "r10-space.txt: $(cat report/r10-space.txt 2>/dev/null)"
fi

STREAM="report/r10-join.ndjson"
cp "${STREAM}" "${RUN_DIR}/r10-join.ndjson" 2>/dev/null || true
if [ ! -s "${STREAM}" ]; then
  add_check "join_stream_is_ndjson" false "no ${STREAM}"
elif jq -e . >/dev/null 2>&1 < "${STREAM}" && [ "$(jq -s 'all(type == "object" and has("type"))' < "${STREAM}")" = true ]; then
  add_check "join_stream_is_ndjson" true "$(wc -l < "${STREAM}") lines, each a JSON object"
else
  add_check "join_stream_is_ndjson" false "not one JSON object per line: $(head -c 300 "${STREAM}")"
fi
TYPES="$(jq -rs 'map(.type) | join(",")' < "${STREAM}" 2>/dev/null)"
if jq -es --arg a "${ADMIN}" '.[0].type == "joined" and .[0].self == $a and .[-1].type == "ended"' >/dev/null 2>&1 < "${STREAM}"; then
  add_check "join_stream_bracketed" true "starts joined as eval-agent, ends with ended ($(jq -rs '.[-1].reason' < "${STREAM}"))"
else
  add_check "join_stream_bracketed" false "types: ${TYPES}"
fi
if jq -es --arg p "${PEER}" 'any(.type == "participant_joined" and .pubkey == $p)' >/dev/null 2>&1 < "${STREAM}"; then
  add_check "join_stream_saw_peer" true "participant_joined ${PEER}"
else
  add_check "join_stream_saw_peer" false "types: ${TYPES}"
fi
if jq -es --arg p "${PEER}" 'any(.type == "chat" and .pubkey == $p)' >/dev/null 2>&1 < "${STREAM}"; then
  add_check "join_stream_saw_chat" true "chat from the peer arrived in the stream"
else
  add_check "join_stream_saw_chat" false "types: ${TYPES}"
fi

CHAT="$(agent_exec "ncli space chat list ${SPACE} --relay ${RELAY} --json" 2>/dev/null)"
if jq -e --arg a "${ADMIN}" '.messages | any(.pubkey == $a and .content == "hello from eval-agent")' >/dev/null 2>&1 <<<"${CHAT}"; then
  add_check "chat_sent" true "eval-agent's message is on the relay"
else
  add_check "chat_sent" false "chat list: $(jq -c '.messages | map({pubkey,content})' <<<"${CHAT}" 2>/dev/null || echo "${CHAT}")"
fi
if jq -e --arg a "${ADMIN}" --arg p "${PEER}" '
    [.messages[] | select(.pubkey == $p) | .id] as $peer
    | .messages | any(.pubkey == $a and .content == "got it" and (.parent as $x | $peer | index($x)))' >/dev/null 2>&1 <<<"${CHAT}"; then
  add_check "threaded_reply" true "\"got it\" replies to the peer's message"
else
  add_check "threaded_reply" false "chat list: $(jq -c '.messages | map({pubkey,content,parent})' <<<"${CHAT}" 2>/dev/null)"
fi

write_verify "${ROUND}" "${RUN_DIR}"
