#!/usr/bin/env bash
# Ground truth for R17. Never trusts the self-report's verdicts:
# - re-runs the agent's own policy through `ncli signer check`;
# - reads the signer's decision log;
# - reads the agent's reply as eval-friend, from the relay;
# - verifies the proof with no key;
# - fetches the reaction from the relay.
set -uo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source bin/lib.sh
RUN_DIR="$1"
ROUND="r17-messaging"
D=/home/evaluser/work/messaging
RELAY=ws://localhost:5500

self_report_exists "${RUN_DIR}" "${ROUND}" \
  && add_check "self_report_written" true "present" \
  || add_check "self_report_written" false "missing"

pub() { agent_exec "ncli id $1 --json" 2>/dev/null | jq -r '.pub_hex // empty'; }
AGENT="$(pub eval-agent)"
FRIEND="$(pub eval-friend)"
STRANGER="$(pub eval-stranger)"
if [ -z "${AGENT}" ] || [ -z "${FRIEND}" ] || [ -z "${STRANGER}" ]; then
  add_check "identities_present" false "agent=${AGENT:-missing} friend=${FRIEND:-missing} stranger=${STRANGER:-missing}"
fi
# Any key stands in for a gift wrap's one-time key.
ONE_TIME="$(agent_exec "ncli id --json" 2>/dev/null | jq -r '.pub_hex // empty')"

# --- the policy, dry-run ---------------------------------------------------------
# check <name> <want allow|deny> <check args...>
check() {
  local name="$1" want="$2"
  shift 2
  local out decision
  out="$(agent_exec "ncli signer check --policy ${D}/policy.yaml --pubkey ${AGENT} --json $*" 2>/dev/null)"
  decision="$(jq -r '.decision // empty' <<<"${out}" 2>/dev/null)"
  if [ "${decision}" = "${want}" ]; then
    add_check "policy_${name}" true "${want} ($(jq -r '.rule // "-"' <<<"${out}"))"
  else
    add_check "policy_${name}" false "want ${want}, got '${decision}': ${out}"
  fi
}
check unwrap_friend allow --method nip44_decrypt --counterpart "${ONE_TIME}" --seal-from "${FRIEND}"
check unwrap_stranger deny --method nip44_decrypt --counterpart "${ONE_TIME}" --seal-from "${STRANGER}"
check decrypt_stranger deny --method nip44_decrypt --counterpart "${STRANGER}" --seal-from "${STRANGER}"
check proof_friend allow --method nip44_message_keys --counterpart "${FRIEND}"
check proof_stranger deny --method nip44_message_keys --counterpart "${STRANGER}"
check encrypt_stranger deny --method nip44_encrypt --counterpart "${STRANGER}"
NOW="$(date +%s)"
probe() { # <name> <want> <event json>
  local out decision
  out="$(agent_exec "printf '%s' '$3' > /tmp/r17-probe.json && ncli signer check --policy ${D}/policy.yaml -e /tmp/r17-probe.json --pubkey ${AGENT} --json" 2>/dev/null)"
  decision="$(jq -r '.decision // empty' <<<"${out}" 2>/dev/null)"
  [ "${decision}" = "$2" ] && add_check "policy_$1" true "$2" || add_check "policy_$1" false "want $2, got '${decision}': ${out}"
}
probe reaction_allowed allow "{\"kind\":7,\"created_at\":${NOW},\"tags\":[],\"content\":\"+\"}"
probe note_denied deny "{\"kind\":1,\"created_at\":${NOW},\"tags\":[],\"content\":\"x\"}"
probe auth_other_relay_denied deny "{\"kind\":22242,\"created_at\":${NOW},\"tags\":[[\"relay\",\"wss://other.example\"],[\"challenge\",\"c\"]],\"content\":\"\"}"

# --- the decision log: the stranger's seal was never opened ------------------------
LOG="$(agent_exec "cat ${D}/decisions.ndjson" 2>/dev/null)"
echo "${LOG}" > "${RUN_DIR}/${ROUND}.decisions.ndjson"
if [ -z "${LOG}" ]; then
  add_check "decision_log_written" false "${D}/decisions.ndjson empty or missing"
else
  add_check "decision_log_written" true "$(wc -l <<<"${LOG}") decisions"
fi
if jq -se --arg s "${STRANGER}" 'any(.[]; .decision=="allow" and (.method|startswith("nip44")) and .counterpart==$s)' >/dev/null 2>&1 <<<"${LOG}"; then
  add_check "stranger_never_decrypted" false "the signer allowed a nip44 request for eval-stranger"
else
  add_check "stranger_never_decrypted" true "no allowed nip44 request for eval-stranger"
fi
if jq -se 'any(.[]; .decision=="deny" and .method=="nip44_decrypt")' >/dev/null 2>&1 <<<"${LOG}"; then
  add_check "stranger_wrap_refused" true "a gift wrap decrypt was denied"
else
  add_check "stranger_wrap_refused" false "no denied nip44_decrypt: was the stranger's wrap ever offered to the signer?"
fi
if jq -se --arg f "${FRIEND}" 'any(.[]; .decision=="allow" and .method=="nip44_message_keys" and .counterpart==$f)' >/dev/null 2>&1 <<<"${LOG}"; then
  add_check "proof_keys_through_signer" true "nip44_message_keys for eval-friend"
else
  add_check "proof_keys_through_signer" false "no allowed nip44_message_keys for eval-friend"
fi

# --- the reply, read back as eval-friend ----------------------------------------------
FROM_AGENT="$(agent_exec "ncli dm read --from eval-agent -s ${RELAY} --signer eval-friend --json" 2>/dev/null)"
FRIEND_MSG="$(agent_exec "ncli dm read --from eval-friend --from eval-agent -s ${RELAY} --signer eval-agent --json" 2>/dev/null \
  | jq -r --arg f "${FRIEND}" '[.[] | select(.from==$f and .content=="ping from friend")] | last | .id // empty')"
if [ -n "${FRIEND_MSG}" ] && jq -e --arg id "${FRIEND_MSG}" 'any(.[]; .content=="pong" and .reply_to==$id)' >/dev/null 2>&1 <<<"${FROM_AGENT}"; then
  add_check "reply_received_by_friend" true "pong answers ${FRIEND_MSG}"
else
  add_check "reply_received_by_friend" false "friend's message '${FRIEND_MSG}', agent's messages: $(jq -c '[.[] | {content, reply_to}]' <<<"${FROM_AGENT}" 2>/dev/null)"
fi

# --- the proof, checked with no key ---------------------------------------------------
cp "report/r17-proof.json" "${RUN_DIR}/${ROUND}-proof.json" 2>/dev/null || true
PROOF="$(cat "report/r17-proof.json" 2>/dev/null)"
if [ -z "${PROOF}" ]; then
  add_check "proof_verifies" false "/report/r17-proof.json missing"
else
  OUT="$(printf '%s' "${PROOF}" | docker compose exec -T agent bash -lc 'export PATH="$HOME/.local/bin:$PATH"; HOME=$(mktemp -d) ncli dm verify - --from '"${FRIEND}"' --json' 2>/dev/null)"
  if jq -e '.verified == true and .message.content == "ping from friend"' >/dev/null 2>&1 <<<"${OUT}"; then
    add_check "proof_verifies" true "verified with no key"
  else
    add_check "proof_verifies" false "dm verify: ${OUT}"
  fi
  CODE="$(printf '%s' "${PROOF}" | docker compose exec -T agent bash -lc 'export PATH="$HOME/.local/bin:$PATH"; HOME=$(mktemp -d) ncli dm verify - --from '"${STRANGER}"' --json >/dev/null 2>&1; echo $?' 2>/dev/null | tr -dc '0-9')"
  [ "${CODE}" = "1" ] && add_check "proof_refuses_other_sender" true "exit 1" || add_check "proof_refuses_other_sender" false "exit ${CODE:-unknown}"
  if grep -q "ping from friend" <<<"${PROOF}"; then
    add_check "proof_has_no_plaintext" false "the proof file carries the message text"
  else
    add_check "proof_has_no_plaintext" true "ciphertext and keys only"
  fi
fi

# --- the reaction -------------------------------------------------------------------------
NOTE="$(agent_exec "ncli find --kinds 1 --authors ${FRIEND} -s ${RELAY} --json" 2>/dev/null | jq -r '[.[] | select(.content=="hello world")] | last | .id // empty')"
REACTION="$(agent_exec "ncli find --kinds 7 --authors ${AGENT} --tag e=${NOTE} -s ${RELAY} --json" 2>/dev/null)"
if [ -n "${NOTE}" ] && jq -e --arg f "${FRIEND}" 'any(.[]; .content=="👀" and any(.tags[]; .[0]=="p" and .[1]==$f) and any(.tags[]; .[0]=="k" and .[1]=="1"))' >/dev/null 2>&1 <<<"${REACTION}"; then
  add_check "reaction_published" true "👀 on ${NOTE}"
else
  add_check "reaction_published" false "note '${NOTE}', reactions: ${REACTION}"
fi

# --- stopped ------------------------------------------------------------------------------
STATUS_EXIT="$(agent_exec "ncli signer status --socket ${D}/agent.sock --json >/dev/null 2>&1; echo \$?" 2>/dev/null | tr -dc '0-9')"
[ "${STATUS_EXIT}" = "6" ] && add_check "signer_stopped" true "status exits 6" || add_check "signer_stopped" false "status exit ${STATUS_EXIT:-unknown}"

write_verify "${ROUND}" "${RUN_DIR}"
