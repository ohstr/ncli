#!/usr/bin/env bash
# Ground truth for R15. Never trusts the self-report's verdicts:
# - dry-runs the agent's own policy through `ncli signer check`;
# - reads the signer's decision log (logins only, nothing else signed);
# - fetches the note back from the members-only relay;
# - re-runs the no-signer and non-member failures itself;
# - cross-checks the reported error reasons against those.
set -uo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source bin/lib.sh
RUN_DIR="$1"
ROUND="r15-relay-login"
D=/home/evaluser/work/login
MEMBERS=ws://localhost:5520
LOGIN_ONLY=ws://localhost:5521

self_report_exists "${RUN_DIR}" "${ROUND}" \
  && add_check "self_report_written" true "present" \
  || add_check "self_report_written" false "missing"

AGENT_PUB="$(agent_exec "ncli id eval-agent --json" 2>/dev/null | jq -r '.pub_hex // empty')"

# --- the policy -----------------------------------------------------------------
# check_policy <name> <want allow|deny> <event json>
check_policy() {
  local name="$1" want="$2" ev="$3" out decision
  out="$(agent_exec "printf '%s' '${ev}' > /tmp/r15-probe.json && ncli signer check --policy ${D}/policy.yaml -e /tmp/r15-probe.json --pubkey ${AGENT_PUB} --json" 2>/dev/null)"
  decision="$(jq -r '.decision // empty' <<<"${out}" 2>/dev/null)"
  if [ "${decision}" = "${want}" ]; then
    add_check "policy_${name}" true "${want} ($(jq -r '.rule // "-"' <<<"${out}"))"
  else
    add_check "policy_${name}" false "want ${want}, got '${decision}': ${out}"
  fi
}
NOW="$(date +%s)"
auth() { echo "{\"kind\":22242,\"created_at\":${NOW},\"tags\":[[\"relay\",\"$1\"],[\"challenge\",\"c\"]],\"content\":\"\"}"; }
check_policy kind1_allowed allow "{\"kind\":1,\"created_at\":${NOW},\"tags\":[],\"content\":\"x\"}"
check_policy login_members_allowed allow "$(auth "${MEMBERS}")"
check_policy login_other_denied deny "$(auth "${LOGIN_ONLY}")"
check_policy kind4_denied deny "{\"kind\":4,\"created_at\":${NOW},\"tags\":[],\"content\":\"x\"}"

# --- the signer is still up, as eval-agent --------------------------------------
STATUS="$(agent_exec "ncli signer status --socket ${D}/agent.sock --json" 2>/dev/null)"
if [ -n "${AGENT_PUB}" ] && [ "$(jq -r '.pubkey // empty' <<<"${STATUS}" 2>/dev/null)" = "${AGENT_PUB}" ]; then
  add_check "signer_running_as_eval_agent" true "${AGENT_PUB}"
else
  add_check "signer_running_as_eval_agent" false "status: ${STATUS:-no answer}"
fi

# --- the decision log: notes and logins only --------------------------------------
LOG="$(agent_exec "cat ${D}/decisions.ndjson" 2>/dev/null)"
echo "${LOG}" > "${RUN_DIR}/${ROUND}.decisions.ndjson"
LOGINS="$(jq -s '[.[] | select(.decision=="allow" and .kind==22242)] | length' <<<"${LOG}" 2>/dev/null || echo 0)"
if [ "${LOGINS:-0}" -ge 2 ]; then
  add_check "logins_signed_through_socket" true "${LOGINS} allowed 22242s"
else
  add_check "logins_signed_through_socket" false "${LOGINS:-0} allowed 22242s (want >= 2: publish and read)"
fi
OTHER="$(jq -sc '[.[] | select(.decision=="allow" and .kind!=1 and .kind!=22242)]' <<<"${LOG}" 2>/dev/null)"
if [ "${OTHER}" = "[]" ]; then
  add_check "nothing_else_signed" true "only kinds 1 and 22242 allowed"
else
  add_check "nothing_else_signed" false "${OTHER}"
fi
if jq -se 'any(.[]; .decision=="deny" and .kind==22242)' >/dev/null 2>&1 <<<"${LOG}"; then
  add_check "other_relay_login_denied_by_signer" true "logged"
else
  add_check "other_relay_login_denied_by_signer" false "no denied 22242 in the decision log"
fi

# --- the note is on the members-only relay, by eval-agent ---------------------------
NOTE_ID="$(self_report_field "${RUN_DIR}" "${ROUND}" '.note_id')"
FOUND="$(agent_exec "ncli find -i ${NOTE_ID:-none} -s ${MEMBERS} --auth-identity eval-agent --json" 2>/dev/null)"
if [ -n "${NOTE_ID}" ] && jq -e --arg id "${NOTE_ID}" --arg pk "${AGENT_PUB}" \
    'type=="array" and length>=1 and .[0].id==$id and .[0].pubkey==$pk and .[0].content=="r15 login note"' >/dev/null 2>&1 <<<"${FOUND}"; then
  add_check "note_published_through_login" true "${NOTE_ID}"
else
  add_check "note_published_through_login" false "note '${NOTE_ID}' not on ${MEMBERS} as eval-agent: ${FOUND}"
fi
if [ -n "${NOTE_ID}" ] && jq -se --arg id "${NOTE_ID}" 'any(.[]; .decision=="allow" and .kind==1 and .event_id==$id)' >/dev/null 2>&1 <<<"${LOG}"; then
  add_check "note_signed_by_socket" true "in the decision log"
else
  add_check "note_signed_by_socket" false "note '${NOTE_ID}' not signed by the signer"
fi

# --- the failures, re-run here ------------------------------------------------------
GOT="$(agent_exec "NCLI_QUERY_IDENTITY= ncli find --kinds 1 -s ${MEMBERS} --json 2>&1 >/dev/null | tail -1" 2>/dev/null)"
if [ "$(jq -r '.reason // empty' <<<"${GOT}" 2>/dev/null)" = "login_required" ]; then
  add_check "no_signer_is_login_required" true "$(jq -c . <<<"${GOT}")"
else
  add_check "no_signer_is_login_required" false "${GOT}"
fi
GOT="$(agent_exec "f=\$(mktemp --suffix=.json); printf '{\"kind\":1,\"created_at\":%s,\"tags\":[],\"content\":\"verifier outsider\"}' \$(date +%s) > \$f; ncli publish -e \$f --signer eval-outsider -s ${MEMBERS} --json 2>&1 >/dev/null | tail -1" 2>/dev/null)"
if [ "$(jq -r '.reason // empty' <<<"${GOT}" 2>/dev/null)" = "relay_refused" ]; then
  add_check "outsider_is_relay_refused" true "$(jq -c . <<<"${GOT}")"
else
  add_check "outsider_is_relay_refused" false "${GOT}"
fi

# --- the reported errors match ------------------------------------------------------
for pair in no_signer:login_required signer_refused:signer_refused relay_refused:relay_refused; do
  key="${pair%%:*}" want="${pair##*:}"
  reason="$(self_report_field "${RUN_DIR}" "${ROUND}" ".errors.${key}.reason")"
  code="$(self_report_field "${RUN_DIR}" "${ROUND}" ".errors.${key}.code")"
  if [ "${reason}" = "${want}" ] && [ "${code}" = "auth" ]; then
    add_check "reported_${key}" true "auth/${reason}"
  else
    add_check "reported_${key}" false "reported code '${code}' reason '${reason}', want auth/${want}"
  fi
done

write_verify "${ROUND}" "${RUN_DIR}"
