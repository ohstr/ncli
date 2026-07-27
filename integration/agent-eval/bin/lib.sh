#!/usr/bin/env bash
# Shared helpers for bin/verify/*.sh. Each verifier's whole job is to
# re-check ground truth itself -- querying the relay/blossom/agent
# containers directly -- rather than trust the agent's own self-report.
# The self-report is still read (self_report_field) so a verifier can
# cross-check "does what it claimed match what's actually true", not to
# take the claim at face value.
set -uo pipefail

CHECKS_JSON="[]"

# add_check <name> <true|false> <detail>
add_check() {
  local name="$1" pass="$2" detail="$3"
  CHECKS_JSON="$(jq -c --arg n "${name}" --argjson p "${pass}" --arg d "${detail}" \
    '. + [{"name":$n,"pass":$p,"detail":$d}]' <<<"${CHECKS_JSON}")"
  if [ "${pass}" = "true" ]; then
    echo "  [pass] ${name}: ${detail}"
  else
    echo "  [FAIL] ${name}: ${detail}"
  fi
}

# write_verify <round> <run_dir>
write_verify() {
  local round="$1" run_dir="$2"
  local verified
  verified="$(jq '(length > 0) and all(.[]; .pass)' <<<"${CHECKS_JSON}")"
  jq -n --arg round "${round}" --argjson checks "${CHECKS_JSON}" --argjson verified "${verified}" \
    '{round:$round, verified:$verified, checks:$checks}' > "${run_dir}/${round}.verify.json"
  echo "verify(${round}): $([ "${verified}" = true ] && echo PASS || echo FAIL)"
  [ "${verified}" = true ]
}

# self_report_field <run_dir> <round> <jq filter>
self_report_field() {
  local run_dir="$1" round="$2" filter="$3"
  jq -r "${filter} // empty" "${run_dir}/${round}.self-report.json" 2>/dev/null
}

self_report_exists() {
  local run_dir="$1" round="$2"
  [ -s "${run_dir}/${round}.self-report.json" ]
}

# agent_exec <command...> -- runs inside the agent container, ncli already on PATH
agent_exec() {
  docker compose exec -T agent bash -lc "export PATH=\"\$HOME/.local/bin:\$PATH\"; $*"
}
