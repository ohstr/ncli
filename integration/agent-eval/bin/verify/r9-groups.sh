#!/usr/bin/env bash
# Ground truth for R9: the relay's own view of r9-team, read as its
# admin, and the moderation events the round should have left behind.
set -uo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source bin/lib.sh
RUN_DIR="$1"
ROUND="r9-groups"
RELAY="ws://localhost:5500"

self_report_exists "${RUN_DIR}" "${ROUND}" \
  && add_check "self_report_written" true "present" \
  || add_check "self_report_written" false "missing"

ADMIN="$(agent_exec 'ncli id eval-agent --json | jq -r .pub_hex' 2>/dev/null)"
MEMBER="$(agent_exec 'ncli id eval-member --json | jq -r .pub_hex' 2>/dev/null)"
if [[ "${MEMBER}" =~ ^[0-9a-f]{64}$ ]]; then
  add_check "member_identity_created" true "eval-member ${MEMBER}"
else
  add_check "member_identity_created" false "ncli id eval-member: ${MEMBER}"
fi

find_group() { # <kinds> <author> -- events in r9-team, read as the admin
  agent_exec "ncli find --kinds $1 --authors $2 --tag h=r9-team -s ${RELAY} --auth-identity eval-agent" 2>/dev/null
}

SHOW="$(agent_exec "ncli groups show r9-team --relay ${RELAY} --identity eval-agent --json" 2>/dev/null)"
if jq -e '.metadata.name == "R9 Team" and .metadata.private == true and .metadata.closed == true and (.metadata.about | length > 0)' >/dev/null 2>&1 <<<"${SHOW}"; then
  add_check "renamed_keeping_privacy" true "R9 Team, still private and closed"
else
  add_check "renamed_keeping_privacy" false "groups show: $(jq -c '.metadata' <<<"${SHOW}" 2>/dev/null || echo "${SHOW}")"
fi
if jq -e --arg a "${ADMIN}" '.admins | any(.pubkey == $a)' >/dev/null 2>&1 <<<"${SHOW}"; then
  add_check "eval_agent_is_admin" true "${ADMIN}"
else
  add_check "eval_agent_is_admin" false "admins: $(jq -c '.admins' <<<"${SHOW}" 2>/dev/null)"
fi

# The relay keeps no moderation events (9xxx), so the checks below read
# the end state: the member left, and its message is gone.
if [[ "${MEMBER}" =~ ^[0-9a-f]{64}$ ]] && ! jq -e --arg m "${MEMBER}" '(.members // []) | index($m)' >/dev/null 2>&1 <<<"${SHOW}"; then
  add_check "member_left" true "eval-member is not in r9-team's roster"
else
  add_check "member_left" false "members: $(jq -c '.members' <<<"${SHOW}" 2>/dev/null)"
fi

POSTS="$(find_group 9 "${MEMBER}")"
CLAIMED="$(self_report_field "${RUN_DIR}" "${ROUND}" '[.. | strings | select(test("^[0-9a-f]{64}$"))] | unique | .[]' \
  | grep -vx -e "${ADMIN}" -e "${MEMBER}")"
SERVED=""
for id in ${CLAIMED}; do
  if agent_exec "ncli find ${id} -s ${RELAY} --auth-identity eval-agent" 2>/dev/null | jq -e 'any(.kind == 9)' >/dev/null 2>&1; then
    SERVED="${id}"
  fi
done
if [ -z "${CLAIMED}" ]; then
  add_check "posted_message_deleted" false "self-report names no event id"
elif [ -n "${SERVED}" ] || ! jq -e 'length == 0' >/dev/null 2>&1 <<<"${POSTS}"; then
  add_check "posted_message_deleted" false "still served: ${SERVED:-$(jq -c 'map(.id)' <<<"${POSTS}")}"
else
  add_check "posted_message_deleted" true "eval-member's message in r9-team is no longer served"
fi

TREE="$(agent_exec "ncli groups tree --relay ${RELAY} --identity eval-agent --json" 2>/dev/null)"
if jq -e '(.nodes | has("r9-team")) and (.nodes | has("r9-notes") | not)' >/dev/null 2>&1 <<<"${TREE}"; then
  add_check "subgroup_deleted" true "r9-team kept, r9-notes gone"
else
  add_check "subgroup_deleted" false "tree: $(jq -c '.nodes | keys' <<<"${TREE}" 2>/dev/null || echo "${TREE}")"
fi
if jq -e '.roots | type == "array"' >/dev/null 2>&1 <<<"${TREE}"; then
  add_check "tree_roots_is_array" true "roots is an array"
else
  add_check "tree_roots_is_array" false "${TREE}"
fi

write_verify "${ROUND}" "${RUN_DIR}"
