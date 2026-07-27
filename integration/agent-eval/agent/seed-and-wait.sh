#!/usr/bin/env bash
# Copies a snapshot of the host's Claude Code login into this container's
# own $HOME, then idles. bin/run.sh drives rounds afterward via
# `docker compose exec agent claude -p ...` -- one fresh `claude` process
# per round, all sharing this same container/filesystem so state an earlier
# round produced (the installed `ncli` binary, its vault, pulled skills)
# is still there for later rounds, the same as a real user's one machine.
#
# Never touches the host's real ~/.claude -- run.sh copies it into
# ./.creds-seed (bind-mounted here read-only as /creds-seed) before compose
# starts, and this script copies *that* into place, so nothing this
# container does can write back to the host's actual Claude Code state.
set -euo pipefail

mkdir -p "$HOME/.claude"

if [ -f /creds-seed/credentials.json ]; then
  cp /creds-seed/credentials.json "$HOME/.claude/.credentials.json"
  chmod 600 "$HOME/.claude/.credentials.json"
else
  echo "seed-and-wait: WARNING -- no /creds-seed/credentials.json mounted; claude will not be authenticated" >&2
fi

if [ -f /creds-seed/claude.json ]; then
  cp /creds-seed/claude.json "$HOME/.claude.json"
fi

exec sleep infinity
