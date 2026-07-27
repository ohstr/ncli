#!/usr/bin/env bash
# Ground truth for R0: is ncli *actually* installed and runnable, verified
# by the harness invoking it directly -- not by trusting the agent's claim.
set -uo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source bin/lib.sh
RUN_DIR="$1"
ROUND="r0-bootstrap"

if self_report_exists "${RUN_DIR}" "${ROUND}"; then
  add_check "self_report_written" true "present"
else
  add_check "self_report_written" false "missing -- agent never wrote it"
fi

VERSION_JSON="$(agent_exec 'ncli version --json' 2>/dev/null)"
if [ -n "${VERSION_JSON}" ] && jq -e '.ncli_version' >/dev/null 2>&1 <<<"${VERSION_JSON}"; then
  NCLI_VER="$(jq -r '.ncli_version' <<<"${VERSION_JSON}")"
  add_check "ncli_installed_and_runnable" true "ncli version --json -> ncli_version=${NCLI_VER}"
else
  add_check "ncli_installed_and_runnable" false "ncli version --json did not return valid JSON with ncli_version"
fi

IDOUT="$(agent_exec 'ncli id --json' 2>/dev/null)"
if jq -e '.npub and .nsec and .pub_hex' >/dev/null 2>&1 <<<"${IDOUT}"; then
  add_check "id_json_sane_shape" true "ncli id --json returns npub/nsec/pub_hex"
else
  add_check "id_json_sane_shape" false "ncli id --json output missing expected fields: ${IDOUT}"
fi

write_verify "${ROUND}" "${RUN_DIR}"
