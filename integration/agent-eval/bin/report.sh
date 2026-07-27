#!/usr/bin/env bash
# Merges each round's self-report (the agent's claim), verify result (the
# harness's independent ground-truth check), and judge verdict (process
# quality) into one report for this run, plus appends a one-line summary
# to report/history.jsonl so runs are diffable over time -- the whole
# point of running this against every release/`:edge` build.
set -uo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_DIR="$1"; shift
ROUNDS=("$@")

ROWS="[]"
for round in "${ROUNDS[@]}"; do
  SELF="$(cat "${RUN_DIR}/${round}.self-report.json" 2>/dev/null || echo 'null')"
  VERIFY="$(cat "${RUN_DIR}/${round}.verify.json" 2>/dev/null || echo 'null')"
  JUDGE="$(cat "${RUN_DIR}/${round}.judge.json" 2>/dev/null || echo 'null')"
  ROW="$(jq -n --arg round "${round}" --argjson self "${SELF}" --argjson verify "${VERIFY}" --argjson judge "${JUDGE}" \
    '{round: $round, self_reported_outcome: ($self.outcome // "missing"), verified: ($verify.verified // false), process_quality: ($judge.process_quality // "unknown"), self_report: $self, verify: $verify, judge: $judge}')"
  ROWS="$(jq -c --argjson row "${ROW}" '. + [$row]' <<<"${ROWS}")"
done

echo "${ROWS}" | jq '.' > "${RUN_DIR}/report.json"

{
  echo "# ncli agent-capability eval -- $(basename "${RUN_DIR}")"
  echo
  echo "| Round | Self-reported | Verified (ground truth) | Process quality |"
  echo "|---|---|---|---|"
  jq -r '.[] | "| \(.round) | \(.self_reported_outcome) | \(.verified) | \(.process_quality) |"' <<<"${ROWS}"
  echo
  echo "\`verified\` comes from bin/verify/*.sh re-checking ground truth"
  echo "directly (relay/blossom state, exit codes, re-derived proof-of-work"
  echo "difficulty, ...), independent of what the agent claimed."
  echo "\`process_quality\` is a separate judge pass over each transcript --"
  echo "see \`<round>.judge.json\` for its notes."
  echo
  echo "## Details"
  echo
  jq -r '.[] |
    "### \(.round)\n\n" +
    "- self-reported outcome: **\(.self_reported_outcome)** -- \(.self_report.summary // "(no summary)")\n" +
    "- verified: **\(.verify.verified // false)**\n" +
    ( (.verify.checks // []) | map("  - " + (if .pass then "[pass] " else "[FAIL] " end) + .name + ": " + .detail) | join("\n") ) + "\n" +
    "- process quality: **\(.process_quality)** -- \(.judge.notes // "(no notes)")\n" +
    ( if ((.judge.flags // []) | length) > 0 then "  - flags: " + ((.judge.flags // []) | join(", ")) else "" end ) +
    "\n"
  ' <<<"${ROWS}"
} > "${RUN_DIR}/report.md"

mkdir -p report
TOTAL="$(jq 'length' <<<"${ROWS}")"
VERIFIED_COUNT="$(jq '[.[] | select(.verified)] | length' <<<"${ROWS}")"
jq -nc --arg run_dir "${RUN_DIR}" --arg date "$(date -u +%FT%TZ)" --argjson total "${TOTAL}" --argjson verified "${VERIFIED_COUNT}" \
  '{date:$date, run_dir:$run_dir, total:$total, verified:$verified}' >> report/history.jsonl

echo "verified ${VERIFIED_COUNT}/${TOTAL} rounds -- see ${RUN_DIR}/report.md"
