#!/usr/bin/env bash
# Ground truth for R18: everything is read back from the relay, and the
# signer's decision log shows the agent signed only through the socket.
set -uo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source bin/lib.sh
RUN_DIR="$1"
ROUND="r18-social"
D=/home/evaluser/work/social
RELAY=ws://localhost:5500

self_report_exists "${RUN_DIR}" "${ROUND}" \
  && add_check "self_report_written" true "present" \
  || add_check "self_report_written" false "missing"

pub() { agent_exec "ncli id $1 --json" 2>/dev/null | jq -r '.pub_hex // empty'; }
AGENT="$(pub eval-agent)"
FRIEND="$(pub eval-friend)"
# newest <kind> <author>: the author's current replaceable event
newest() {
  agent_exec "ncli find --kinds $1 --authors $2 -s ${RELAY} --json" 2>/dev/null | jq -c 'sort_by(.created_at) | last // empty'
}

# --- profile: merged, lightning address kept -------------------------------------
PROFILE="$(newest 0 "${AGENT}" | jq -r '.content // empty')"
if jq -e '.name=="eval-agent" and .about=="I answer messages quickly" and .lud16=="agent@pay.example"' >/dev/null 2>&1 <<<"${PROFILE}"; then
  add_check "profile_merged" true "name, about and lud16 all present"
else
  add_check "profile_merged" false "kind 0 content: ${PROFILE:-none}"
fi

# --- relay lists ---------------------------------------------------------------------
NIP65="$(newest 10002 "${AGENT}")"
if jq -e --arg r "${RELAY}" 'any(.tags[]; .[0]=="r" and (.[1]|rtrimstr("/"))==$r and (length==2))' >/dev/null 2>&1 <<<"${NIP65}"; then
  add_check "relay_list" true "read and write"
else
  add_check "relay_list" false "10002: ${NIP65:-none}"
fi
DMREL="$(newest 10050 "${AGENT}")"
if jq -e --arg r "${RELAY}" 'any(.tags[]; .[0]=="relay" and (.[1]|rtrimstr("/"))==$r)' >/dev/null 2>&1 <<<"${DMREL}"; then
  add_check "dm_relay_list" true "10050 names the relay"
else
  add_check "dm_relay_list" false "10050: ${DMREL:-none}"
fi

# --- follows -----------------------------------------------------------------------------
FOLLOWS="$(newest 3 "${AGENT}")"
if jq -e --arg f "${FRIEND}" 'any(.tags[]; .[0]=="p" and .[1]==$f)' >/dev/null 2>&1 <<<"${FOLLOWS}"; then
  add_check "follows_friend" true "kind 3 has eval-friend"
else
  add_check "follows_friend" false "kind 3: ${FOLLOWS:-none}"
fi

# --- reply, note, deletion ---------------------------------------------------------------
QUESTION="$(agent_exec "ncli find --kinds 1 --authors ${FRIEND} -s ${RELAY} --json" 2>/dev/null | jq -r '[.[] | select(.content=="what do you think?")] | last | .id // empty')"
REPLY="$(agent_exec "ncli find --kinds 1 --authors ${AGENT} --tag e=${QUESTION} -s ${RELAY} --json" 2>/dev/null)"
if [ -n "${QUESTION}" ] && jq -e --arg q "${QUESTION}" --arg f "${FRIEND}" 'any(.[]; .content=="looks good" and any(.tags[]; .[0]=="e" and .[1]==$q and .[3]=="root") and any(.tags[]; .[0]=="p" and .[1]==$f))' >/dev/null 2>&1 <<<"${REPLY}"; then
  add_check "reply_threaded" true "NIP-10 root and p tags"
else
  add_check "reply_threaded" false "question '${QUESTION}', replies: ${REPLY}"
fi
DELS="$(agent_exec "ncli find --kinds 5 --authors ${AGENT} -s ${RELAY} --json" 2>/dev/null)"
DELETED="$(jq -r '[.[] | .tags[] | select(.[0]=="e") | .[1]] | .[]' <<<"${DELS}" 2>/dev/null)"
STILL="$(agent_exec "ncli find --kinds 1 --authors ${AGENT} -s ${RELAY} --json" 2>/dev/null | jq -r '[.[] | select(.content=="test note")] | length')"
if [ -n "${DELETED}" ] && [ "${STILL}" = "0" ]; then
  add_check "note_deleted" true "kind 5 published, the note is gone"
else
  add_check "note_deleted" false "deletions: $(jq -c 'length' <<<"${DELS}" 2>/dev/null), test notes still served: ${STILL}"
fi

# --- the group ---------------------------------------------------------------------------
CHAT="$(agent_exec "ncli groups read r18-room --relay ${RELAY} --identity eval-friend --json" 2>/dev/null)"
MENTION="$(jq -r --arg f "${FRIEND}" --arg a "${AGENT}" '[.[] | select(.from==$f and (.mentions|index($a)))] | last | .id // empty' <<<"${CHAT}" 2>/dev/null)"
if [ -n "${MENTION}" ] && jq -e --arg a "${AGENT}" --arg m "${MENTION}" 'any(.[]; .from==$a and .content=="on it" and .reply_to==$m)' >/dev/null 2>&1 <<<"${CHAT}"; then
  add_check "group_answer" true "on it, quoting ${MENTION}"
else
  add_check "group_answer" false "mention '${MENTION}', messages: $(jq -c '[.[] | {from, content, reply_to}]' <<<"${CHAT}" 2>/dev/null)"
fi

# --- signed through the socket only -------------------------------------------------------
LOG="$(agent_exec "cat ${D}/decisions.ndjson" 2>/dev/null)"
echo "${LOG}" > "${RUN_DIR}/${ROUND}.decisions.ndjson"
for k in 0 3 10002 10050 1 5 9; do
  if jq -se --argjson k "${k}" 'any(.[]; .decision=="allow" and .kind==$k)' >/dev/null 2>&1 <<<"${LOG}"; then
    add_check "signed_kind_${k}_through_signer" true "logged"
  else
    add_check "signed_kind_${k}_through_signer" false "no allowed kind ${k} in the decision log"
  fi
done
NOW="$(date +%s)"
OUT="$(agent_exec "printf '%s' '{\"kind\":4,\"created_at\":${NOW},\"tags\":[],\"content\":\"x\"}' > /tmp/r18-probe.json && ncli signer check --policy ${D}/policy.yaml -e /tmp/r18-probe.json --pubkey ${AGENT} --json" 2>/dev/null)"
[ "$(jq -r '.decision // empty' <<<"${OUT}")" = "deny" ] && add_check "policy_denies_other_kinds" true "kind 4 denied" || add_check "policy_denies_other_kinds" false "${OUT}"

STATUS_EXIT="$(agent_exec "ncli signer status --socket ${D}/agent.sock --json >/dev/null 2>&1; echo \$?" 2>/dev/null | tr -dc '0-9')"
[ "${STATUS_EXIT}" = "6" ] && add_check "signer_stopped" true "status exits 6" || add_check "signer_stopped" false "status exit ${STATUS_EXIT:-unknown}"

write_verify "${ROUND}" "${RUN_DIR}"
