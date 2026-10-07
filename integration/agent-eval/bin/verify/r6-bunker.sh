#!/usr/bin/env bash
# Ground truth for R6: the NIP-46 counterparty fixture (run by bin/run.sh,
# not the agent) reports what the bunker actually answered -- the granted
# kind 1, the kind 7 the agent was to approve, the kind 30023 it was to
# reject -- and the daemon's own state after the agent stopped it.
set -uo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source bin/lib.sh
RUN_DIR="$1"
ROUND="r6-bunker"

self_report_exists "${RUN_DIR}" "${ROUND}" \
  && add_check "self_report_written" true "present" \
  || add_check "self_report_written" false "missing"

if [ -s "${RUN_DIR}/r6-bunker-uri.txt" ]; then
  add_check "pairing_uri_produced" true "$(cat "${RUN_DIR}/r6-bunker-uri.txt")"
else
  add_check "pairing_uri_produced" false "no r6-bunker-uri.txt was captured"
fi

# The agent started the daemon itself: no pty workaround in its commands.
TRANSCRIPT="${RUN_DIR}/${ROUND}.transcript.jsonl"
CMDS="$(jq -r 'select(.type=="assistant") | .message.content[]? | select(.type=="tool_use" and .name=="Bash") | .input.command' "${TRANSCRIPT}" 2>/dev/null)"
if grep -qE '(^|[;&| ])(script|unbuffer|expect)( |$)|socat .*pty' <<<"${CMDS}"; then
  add_check "started_headless_without_tty_tricks" false "transcript uses a pty wrapper"
elif grep -qE 'ncli bunker( +--[a-z-]+(=[^ ]+| +[^- ][^ ]*)?)* *($|[;&|>)]|2>)' <<<"${CMDS}"; then
  add_check "started_headless_without_tty_tricks" true "bare ncli bunker run directly"
else
  add_check "started_headless_without_tty_tricks" false "no bare ncli bunker start found in the transcript"
fi

FIXTURE="$(grep -E '^\{' "${RUN_DIR}/r6-bunker.fixture.json" 2>/dev/null | tail -1)"
if jq -e '.ok == true and (.signed_event.sig | length) == 128' >/dev/null 2>&1 <<<"${FIXTURE}"; then
  add_check "granted_kind1_signed" true "fixture paired and got kind 1 signed under the grant"
else
  add_check "granted_kind1_signed" false "fixture output: ${FIXTURE:-$(head -c 400 "${RUN_DIR}/r6-bunker.fixture.json" 2>/dev/null)}"
fi
if jq -e '.approve_kind7.signed_event.kind == 7 and (.approve_kind7.signed_event.sig | length) == 128' >/dev/null 2>&1 <<<"${FIXTURE}"; then
  add_check "pending_kind7_approved" true "kind 7 signed after the agent approved it"
else
  add_check "pending_kind7_approved" false "$(jq -c '.approve_kind7' <<<"${FIXTURE}" 2>/dev/null)"
fi
if jq -e '.reject_kind30023.error and (.reject_kind30023.error | test("no decision") | not) and (.reject_kind30023.signed_event == null)' >/dev/null 2>&1 <<<"${FIXTURE}"; then
  add_check "pending_kind30023_rejected" true "$(jq -r '.reject_kind30023.error' <<<"${FIXTURE}")"
else
  add_check "pending_kind30023_rejected" false "$(jq -c '.reject_kind30023' <<<"${FIXTURE}" 2>/dev/null)"
fi

STATUS="$(agent_exec 'ncli bunker status --json' 2>/dev/null)"
if jq -e '.running == false' >/dev/null 2>&1 <<<"${STATUS}"; then
  add_check "signer_stopped" true "status: not running"
else
  add_check "signer_stopped" false "status: ${STATUS}"
  agent_exec 'ncli bunker stop --json' >/dev/null 2>&1 || true
fi

# The session was revoked: sessions.yaml no longer holds the app's pubkey.
CLIENT="$(jq -r '.client_pubkey // empty' <<<"${FIXTURE}" 2>/dev/null)"
SESSIONS_FILE="$(agent_exec 'cat "$(ncli version --json | jq -r .app_data_dir)/bunker/sessions.yaml" 2>/dev/null')"
if [ -z "${CLIENT}" ]; then
  add_check "session_revoked" false "no client pubkey from the fixture"
elif grep -q "${CLIENT}" <<<"${SESSIONS_FILE}"; then
  add_check "session_revoked" false "sessions.yaml still holds ${CLIENT}"
else
  add_check "session_revoked" true "${CLIENT} no longer remembered"
fi

write_verify "${ROUND}" "${RUN_DIR}"
