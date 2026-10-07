#!/usr/bin/env bash
# Ground truth for R3: the shared relay's own admin state, queried
# directly -- not the agent's account of it. The strongest signal here is
# cleanup: members/invites the agent created and then removed should
# actually be gone.
set -uo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source bin/lib.sh
RUN_DIR="$1"
ROUND="r3-relay-ops"

self_report_exists "${RUN_DIR}" "${ROUND}" \
  && add_check "self_report_written" true "present" \
  || add_check "self_report_written" false "missing"

STATS="$(agent_exec 'ncli relay stats --config /relay/relay.yaml --json' 2>/dev/null)"
if jq -e '.status == "active"' >/dev/null 2>&1 <<<"${STATS}"; then
  add_check "shared_relay_admin_reachable" true "ncli relay stats reports status=active"
else
  add_check "shared_relay_admin_reachable" false "ncli relay stats: ${STATS}"
fi

MEMBERS="$(agent_exec 'ncli relay members list --config /relay/relay.yaml --json' 2>/dev/null)"
if jq -e '(.members // []) | length == 0' >/dev/null 2>&1 <<<"${MEMBERS}"; then
  add_check "member_cleanup_left_no_leftovers" true "no enrolled members remain"
else
  add_check "member_cleanup_left_no_leftovers" false "members list not empty: ${MEMBERS}"
fi

INVITES="$(agent_exec 'ncli relay invites list --config /relay/relay.yaml --json' 2>/dev/null)"
if jq -e '(.invites // []) | length == 0' >/dev/null 2>&1 <<<"${INVITES}"; then
  add_check "invite_cleanup_left_no_leftovers" true "no invite codes remain"
else
  add_check "invite_cleanup_left_no_leftovers" false "invites list not empty: ${INVITES}"
fi

ROLES="$(agent_exec 'ncli relay roles list --config /relay/relay.yaml --json' 2>/dev/null)"
if jq -e '(.roles // []) | length >= 1' >/dev/null 2>&1 <<<"${ROLES}"; then
  add_check "role_actually_created" true "at least one role exists: $(jq -c '.roles' <<<"${ROLES}")"
else
  add_check "role_actually_created" false "roles list: ${ROLES}"
fi

# Part A ran its relay with --json: every stderr line must be JSON.
LOG="report/r3-relay-stderr.log"
cp "${LOG}" "${RUN_DIR}/r3-relay-stderr.log" 2>/dev/null || true
if [ ! -s "${LOG}" ]; then
  add_check "relay_json_logs_are_json" false "no ${LOG} captured"
else
  BAD="$(grep -v '^[[:space:]]*$' "${LOG}" | while IFS= read -r l; do jq -e . >/dev/null 2>&1 <<<"${l}" || echo "${l}"; done | head -3)"
  if [ -z "${BAD}" ] && grep -q 'listening' "${LOG}"; then
    add_check "relay_json_logs_are_json" true "$(grep -vc '^[[:space:]]*$' "${LOG}") lines, all JSON"
  else
    add_check "relay_json_logs_are_json" false "non-JSON lines: ${BAD:-none, but no listening line}"
  fi
fi

CONTEXTS="$(agent_exec 'ncli relay context list --json' 2>/dev/null)"
if jq -e '(.contexts | type == "object") and (.contexts | length == 0)' >/dev/null 2>&1 <<<"${CONTEXTS}"; then
  add_check "context_removed" true "no relay contexts remain"
else
  add_check "context_removed" false "context list: ${CONTEXTS}"
fi

SEARCH="$(agent_exec 'ncli relay reindex search --config /relay/relay.yaml --json 2>&1 >/dev/null; echo "exit=$?"' 2>/dev/null)"
if grep -q '"code":"usage"' <<<"${SEARCH}" && grep -q 'exit=2' <<<"${SEARCH}"; then
  add_check "search_off_is_usage" true "reindex search without search: usage, exit 2"
else
  add_check "search_off_is_usage" false "${SEARCH}"
fi

write_verify "${ROUND}" "${RUN_DIR}"
