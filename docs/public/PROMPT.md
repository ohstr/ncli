# ncli — agent bootstrap prompt

You've been pointed at this file (by a user, or by another agent) to install
and start using `ncli`, a Nostr relay server and CLI toolkit, in a project
that has no local copy of this repo. Follow the steps in order.

## 1. Install

Skip this if it's already on `PATH`:

```sh
command -v ncli && ncli version
```

Otherwise, pick one for the current OS:

**macOS / Linux**

```sh
curl -fsSL https://ohstr.github.io/ncli/install.sh | sh
```

**Windows (PowerShell)**

```powershell
irm https://ohstr.github.io/ncli/install.ps1 | iex
```

**Homebrew (macOS/Linux)**

```sh
brew install ohstr/tap/ncli
```

**go install**

```sh
go install github.com/ohstr/ncli/cmd/ncli@latest
```

**Docker** (no toolchain required)

```sh
docker run --rm ghcr.io/ohstr/ncli:latest --help
```

## 2. Confirm it works

```sh
ncli id --json
```

Generates a throwaway keypair and prints it as JSON — no network, no
config, no vault password needed. Valid JSON back means the install is
good. If it fails, `ncli version` shows what actually got installed.

## 3. Load the real reference

`ncli` runs a Nostr relay, and streams/syncs/queries/dumps/publishes/mines
events against one, all driven by YAML spec/config files.

Before running any real command, fetch the full command table and the
`--json`/error-code contract — it's the source of truth, don't guess at it
from this file:

```
https://raw.githubusercontent.com/ohstr/ncli/main/AGENTS.md
```

## 4. Pull in task-specific skills

For YAML examples and gotchas beyond `--help`, install the skills instead
of re-deriving them:

```sh
# any agentskills.io-compatible agent
npx skills add ohstr/ncli --all -y

# Claude Code only
/plugin marketplace add ohstr/ncli
```

AGENTS.md (step 3) points to which one to read for a given task.
