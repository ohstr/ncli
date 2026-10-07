#!/usr/bin/env bash
# Which ncli commands did agents actually run this run? Walks the command
# tree of the ncli inside the agent container, counts each command's uses
# across the run's transcripts, and writes coverage.md. Exits non-zero if
# a command outside coverage-allow.txt was never run.
#
#   bin/coverage.sh <run_dir>
set -uo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source bin/lib.sh
RUN_DIR="$1"

# shellcheck disable=SC2016
TREE="$(agent_exec '
walk() {
  local subs s
  subs=$(ncli "$@" --help 2>&1 | awk "/^Available Commands:/{f=1;next} /^[^ ]/{f=0} f&&NF{print \$1}" | grep -vx "help\|completion")
  for s in $subs; do echo "$* $s" | sed "s/^ //"; walk "$@" "$s"; done
}
walk' 2>/dev/null)"
if [ -z "${TREE}" ]; then
  echo "coverage: could not walk ncli's command tree" >&2
  exit 1
fi

CMDS="$(mktemp)"
trap 'rm -f "${CMDS}"' EXIT
for f in "${RUN_DIR}"/*.transcript.jsonl; do
  jq -r 'select(.type=="assistant") | .message.content[]? | select(.type=="tool_use" and .name=="Bash") | .input.command' "${f}" 2>/dev/null
done > "${CMDS}"

ALLOW="$(grep -v '^#' coverage-allow.txt 2>/dev/null | sed '/^$/d')"
missing=()
{
  echo "## Command coverage"
  echo
  echo "| command | runs |"
  echo "|---|---|"
  while IFS= read -r c; do
    n="$(grep -cE "ncli ${c}( |\$|\"|'|\\)|;|\\|)" "${CMDS}")"
    echo "| \`${c}\` | ${n} |"
    if [ "${n}" -eq 0 ] && ! grep -qxF "${c}" <<<"${ALLOW}"; then
      missing+=("${c}")
    fi
  done <<<"${TREE}"
  echo
  if [ "${#missing[@]}" -eq 0 ]; then
    echo "Every command was run at least once (allowed exceptions: $(tr '\n' ' ' <<<"${ALLOW}"))."
  else
    echo "**Never run:** $(printf '`%s` ' "${missing[@]}")"
  fi
} > "${RUN_DIR}/coverage.md"

total="$(wc -l <<<"${TREE}")"
echo "coverage: $((total - ${#missing[@]}))/${total} commands run$( [ "${#missing[@]}" -gt 0 ] && printf '; never run: %s' "${missing[*]}")"
[ "${#missing[@]}" -eq 0 ]
