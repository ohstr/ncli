# The agent-under-test's machine. Deliberately minimal and deliberately
# does NOT contain ncli, Go, or a copy of the ncli source tree -- the whole
# point of this harness is that the agent's only knowledge of ncli comes
# from what it fetches at runtime (docs/PROMPT.md -> AGENTS.md -> skills),
# the same as a real stranger picking it up for the first time.
#
# Node is here for two reasons that have nothing to do with ncli itself:
# PROMPT.md's own step 4 (`npx skills add ohstr/ncli --all -y`) needs it,
# and the R6 bunker round's NIP-46 counterparty fixture is a small Node
# script (see ../fixtures).
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
      bash curl ca-certificates jq git unzip gnupg xz-utils procps \
    && rm -rf /var/lib/apt/lists/*

ARG NODE_MAJOR=22
RUN curl -fsSL https://deb.nodesource.com/setup_${NODE_MAJOR}.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*

# Non-root on purpose: Claude Code refuses --dangerously-skip-permissions /
# permission-mode bypassPermissions as root, and running the agent-under-test
# as root inside a throwaway container is the wrong default regardless.
RUN useradd -m -u 10001 -s /bin/bash evaluser
USER evaluser
WORKDIR /home/evaluser

RUN curl -fsSL https://claude.ai/install.sh | bash
ENV PATH="/home/evaluser/.local/bin:${PATH}"

# Deps for fixtures/nip46-client.cjs (R6's NIP-46 counterparty). Baked in
# at a fixed path rather than installed under the /fixtures bind mount, so
# mounting that directory read-only at runtime doesn't require an
# `npm install` step in the container first. CommonJS + NODE_PATH is what
# makes this reachable from the mounted .cjs file without a bundler.
RUN mkdir -p /home/evaluser/fixture-deps && cd /home/evaluser/fixture-deps \
    && npm init -y >/dev/null \
    && npm install nostr-tools@2.24.1 >/dev/null
ENV NODE_PATH=/home/evaluser/fixture-deps/node_modules

COPY --chown=evaluser:evaluser seed-and-wait.sh /home/evaluser/seed-and-wait.sh
RUN chmod +x /home/evaluser/seed-and-wait.sh && mkdir -p /home/evaluser/work

WORKDIR /home/evaluser/work
ENTRYPOINT ["/home/evaluser/seed-and-wait.sh"]
