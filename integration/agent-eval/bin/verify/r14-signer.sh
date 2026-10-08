#!/usr/bin/env bash
# Ground truth for R14. Never trusts the self-report's verdicts:
# - re-runs the agent's own policy through `ncli signer check`;
# - reads the signer's decision log and used-attestation file;
# - fetches the signed events back from the relay;
# - checks the approval event's author/content itself;
# - confirms the signer is stopped.
set -uo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source bin/lib.sh
RUN_DIR="$1"
ROUND="r14-signer"
D=/home/evaluser/work/signer
RELAY=ws://localhost:5500

self_report_exists "${RUN_DIR}" "${ROUND}" \
  && add_check "self_report_written" true "present" \
  || add_check "self_report_written" false "missing"

AGENT_PUB="$(agent_exec "ncli id eval-agent --json" 2>/dev/null | jq -r '.pub_hex // empty')"
AGENT_PRIV="$(agent_exec "ncli id eval-agent --reveal --json" 2>/dev/null | jq -r '.priv_hex // empty')"
MAINT_PUB="$(agent_exec "ncli id eval-maintainer --json" 2>/dev/null | jq -r '.pub_hex // empty')"
if [ -z "${AGENT_PUB}" ] || [ -z "${MAINT_PUB}" ]; then
  add_check "identities_present" false "eval-agent=${AGENT_PUB:-missing} eval-maintainer=${MAINT_PUB:-missing}"
fi

# --- the policy, dry-run against fixed probes ------------------------------
# check <name> <want allow|deny> <event json>
check_policy() {
  local name="$1" want="$2" ev="$3" out decision
  out="$(agent_exec "printf '%s' '${ev}' > /tmp/r14-probe.json && ncli signer check --policy ${D}/policy.yaml -e /tmp/r14-probe.json --pubkey ${AGENT_PUB} --json" 2>/dev/null)"
  decision="$(jq -r '.decision // empty' <<<"${out}" 2>/dev/null)"
  if [ "${decision}" = "${want}" ]; then
    add_check "policy_${name}" true "${want} ($(jq -r '.rule // "-"' <<<"${out}"))"
  else
    add_check "policy_${name}" false "want ${want}, got '${decision}': ${out}"
  fi
}
NOW="$(date +%s)"
check_policy kind1_allowed allow "{\"kind\":1,\"created_at\":${NOW},\"tags\":[],\"content\":\"x\"}"
check_policy kind7_allowed allow "{\"kind\":7,\"created_at\":${NOW},\"tags\":[],\"content\":\"+\"}"
check_policy kind4_denied deny "{\"kind\":4,\"created_at\":${NOW},\"tags\":[],\"content\":\"x\"}"
check_policy kind0_denied deny "{\"kind\":0,\"created_at\":${NOW},\"tags\":[],\"content\":\"{}\"}"
check_policy auth_own_relay_allowed allow "{\"kind\":22242,\"created_at\":${NOW},\"tags\":[[\"relay\",\"ws://localhost:5500\"],[\"challenge\",\"c\"]],\"content\":\"\"}"
check_policy auth_other_relay_denied deny "{\"kind\":22242,\"created_at\":${NOW},\"tags\":[[\"relay\",\"wss://other.example\"],[\"challenge\",\"c\"]],\"content\":\"\"}"
check_policy auth_lookalike_denied deny "{\"kind\":22242,\"created_at\":${NOW},\"tags\":[[\"relay\",\"ws://localhost:55001\"],[\"challenge\",\"c\"]],\"content\":\"\"}"
check_policy state_without_approval_denied deny "{\"kind\":30618,\"created_at\":${NOW},\"tags\":[[\"d\",\"eval-repo\"]],\"content\":\"\"}"

# --- the decision log --------------------------------------------------------
LOG="$(agent_exec "cat ${D}/decisions.ndjson" 2>/dev/null)"
echo "${LOG}" > "${RUN_DIR}/${ROUND}.decisions.ndjson"
if [ -z "${LOG}" ]; then
  add_check "decision_log_written" false "${D}/decisions.ndjson empty or missing"
else
  add_check "decision_log_written" true "$(wc -l <<<"${LOG}") decisions"
fi
if [ -n "${AGENT_PRIV}" ] && grep -qi "${AGENT_PRIV}" <<<"${LOG}"; then
  add_check "decision_log_has_no_key" false "the agent's private key appears in the decision log"
else
  add_check "decision_log_has_no_key" true "no key material"
fi

NOTE_ID="$(jq -rs '[.[] | select(.decision=="allow" and .kind==1)] | last | .event_id // empty' <<<"${LOG}" 2>/dev/null)"
STATE_ID="$(jq -rs '[.[] | select(.decision=="allow" and .kind==30618 and ((.attestations // []) | length) > 0)] | last | .event_id // empty' <<<"${LOG}" 2>/dev/null)"
USED_ATT="$(jq -rs '[.[] | select(.decision=="allow" and .kind==30618)] | last | .attestations[0] // empty' <<<"${LOG}" 2>/dev/null)"

if jq -se 'any(.[]; .decision=="deny" and .kind==0)' >/dev/null 2>&1 <<<"${LOG}"; then
  add_check "profile_update_denied" true "kind 0 denied by the signer"
else
  add_check "profile_update_denied" false "no kind 0 denial in the decision log"
fi
if jq -se 'any(.[]; .decision=="deny" and .kind==30618 and (.reason|test("required")))' >/dev/null 2>&1 <<<"${LOG}"; then
  add_check "state_denied_without_approval" true "logged"
else
  add_check "state_denied_without_approval" false "no 'attestation required' denial for kind 30618"
fi
if jq -se 'any(.[]; .decision=="deny" and .kind==30618 and (.reason|test("already used")))' >/dev/null 2>&1 <<<"${LOG}"; then
  add_check "approval_replay_denied" true "logged"
else
  add_check "approval_replay_denied" false "no 'already used' denial for kind 30618"
fi

# --- signed events are on the relay, signed by eval-agent --------------------
on_relay() {
  local id="$1" kind="$2" found
  found="$(agent_exec "ncli find -i ${id} -s ${RELAY}" 2>/dev/null)"
  jq -e --arg id "${id}" --arg pk "${AGENT_PUB}" --argjson k "${kind}" \
    'type=="array" and length>=1 and .[0].id==$id and .[0].pubkey==$pk and .[0].kind==$k' >/dev/null 2>&1 <<<"${found}"
}
if [ -n "${NOTE_ID}" ] && on_relay "${NOTE_ID}" 1; then
  add_check "note_signed_via_socket_and_published" true "${NOTE_ID}"
else
  add_check "note_signed_via_socket_and_published" false "allowed kind 1 id '${NOTE_ID}' not found on the relay as eval-agent"
fi
if [ -n "${STATE_ID}" ] && on_relay "${STATE_ID}" 30618; then
  add_check "state_signed_with_approval_and_published" true "${STATE_ID}"
else
  add_check "state_signed_with_approval_and_published" false "approved kind 30618 id '${STATE_ID}' not found on the relay as eval-agent"
fi

# --- the approval: right author, binds this exact event, consumed -----------
cp "report/${ROUND}-approval.json" "${RUN_DIR}/${ROUND}-approval.json" 2>/dev/null || true
APPROVAL="$(cat "report/${ROUND}-approval.json" 2>/dev/null)"
APPROVAL_ID="$(jq -r 'if type=="array" then .[0] else . end | .id // empty' <<<"${APPROVAL}" 2>/dev/null)"
if jq -e --arg pk "${MAINT_PUB}" --arg sid "${STATE_ID}" \
    'if type=="array" then .[0] else . end | .kind==9 and .pubkey==$pk and (.content|gsub("^\\s+|\\s+$";""))==("approve " + $sid)' \
    >/dev/null 2>&1 <<<"${APPROVAL}"; then
  add_check "approval_by_maintainer_for_this_event" true "${APPROVAL_ID}"
else
  add_check "approval_by_maintainer_for_this_event" false "approval file missing or not a kind 9 'approve ${STATE_ID}' by eval-maintainer"
fi
USED="$(agent_exec "cat ${D}/state/used-attestations.ndjson" 2>/dev/null)"
if [ -n "${APPROVAL_ID}" ] && [ "${APPROVAL_ID}" = "${USED_ATT}" ] && grep -q "${APPROVAL_ID}" <<<"${USED}"; then
  add_check "approval_consumed_by_signer" true "in the decision log and ${D}/state"
else
  add_check "approval_consumed_by_signer" false "approval '${APPROVAL_ID}', logged attestation '${USED_ATT}', used set: ${USED:-empty}"
fi

# --- stopped -------------------------------------------------------------------
STATUS_EXIT="$(agent_exec "ncli signer status --socket ${D}/agent.sock --json >/dev/null 2>&1; echo \$?" 2>/dev/null | tr -dc '0-9')"
if [ "${STATUS_EXIT}" = "6" ]; then
  add_check "signer_stopped" true "status exits 6"
else
  add_check "signer_stopped" false "status exit ${STATUS_EXIT:-unknown}"
fi

write_verify "${ROUND}" "${RUN_DIR}"
