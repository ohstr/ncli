#!/usr/bin/env bash
# Ground truth for R1: does the vault actually contain `eval-agent`, and
# does its npub/pubkey round-trip through `ncli decode` correctly.
set -uo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source bin/lib.sh
RUN_DIR="$1"
ROUND="r1-identity"

self_report_exists "${RUN_DIR}" "${ROUND}" \
  && add_check "self_report_written" true "present" \
  || add_check "self_report_written" false "missing"

LOOKUP="$(agent_exec 'ncli id eval-agent --json' 2>/dev/null)"
if jq -e '.saved == true and .label == "eval-agent"' >/dev/null 2>&1 <<<"${LOOKUP}"; then
  add_check "vault_label_saved" true "eval-agent is a saved vault entry"
else
  add_check "vault_label_saved" false "ncli id eval-agent --json: ${LOOKUP}"
fi

NPUB="$(jq -r '.npub // empty' <<<"${LOOKUP}")"
PUBHEX="$(jq -r '.pub_hex // empty' <<<"${LOOKUP}")"
if [ -n "${NPUB}" ] && [ -n "${PUBHEX}" ]; then
  DECODED="$(agent_exec "ncli decode ${NPUB} --json" 2>/dev/null)"
  DECODED_HEX="$(jq -r '.pub_hex // empty' <<<"${DECODED}")"
  if [ "${DECODED_HEX}" = "${PUBHEX}" ]; then
    add_check "npub_decodes_to_same_pubkey" true "decode(${NPUB}) == ${PUBHEX}"
  else
    add_check "npub_decodes_to_same_pubkey" false "decode(${NPUB}) -> '${DECODED_HEX}', expected ${PUBHEX}"
  fi
else
  add_check "npub_decodes_to_same_pubkey" false "could not read npub/pub_hex from vault lookup"
fi

write_verify "${ROUND}" "${RUN_DIR}"
