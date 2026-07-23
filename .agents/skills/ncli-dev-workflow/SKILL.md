---
name: ncli-dev-workflow
description: ncli-specific overlay on top of the org-wide ohstr/dev-workflow skill -- this repo's actual branch-protection ruleset, commit-prefix convention, the GOWORK=off build gotcha, the Co-Authored-By policy, and the concrete release.yml/GoReleaser/homebrew mechanics for cutting a release. Read the generic skill first for the workflow shape (worktree-first, issue-vs-trivial, atomic commits, draft-PR mechanics, release-cut checklist); read this one for what's actually true of ohstr/ncli. Use before starting any change to this source tree (not the ncli binary's own commands -- see skills/ for those), and always before committing, pushing, or opening a PR against ohstr/ncli.
---

<!-- Process doc, not a command reference. Update by hand when the workflow
changes -- nothing here is derived automatically from the repo. This is the
ncli-specific overlay; the shared shape lives in the standalone
ohstr/dev-workflow repo (https://github.com/ohstr/dev-workflow), installed
locally at /u/flzpace/xgit/orgs/ohstr/dev-workflow -- its own git repo, not
git-tracked here, since it's shared across ohstr repos, not ncli-owned.
Read `skills/dev-workflow/SKILL.md` there first; this file only states
what's specific to ncli. -->

# Contributing to ncli

Read `/u/flzpace/xgit/orgs/ohstr/dev-workflow/skills/dev-workflow/SKILL.md`
(the `ohstr/dev-workflow` repo's core skill) first for the full shape:
worktrees, the issue-vs-trivial decision, atomic commits, draft-PR
mechanics, merge policy, and the release-cut checklist. This file only
fills in what that doc leaves as "check the repo": the parts that are
actually specific to ohstr/ncli.

`main` is protected -- nothing lands there except through a merged PR. See
"`main` branch protection" below for the live ruleset. Unlike `skills/*`
(binary-only, written for agents/users who only have the `ncli` binary on
PATH) and `.claude/skills/verify` (local build/run verification, not
process), this file and the org-wide doc it overlays assume git/gh access
to this source tree.

## Build/test command (step 3 of the generic doc)

```sh
GOWORK=off go build ./... && GOWORK=off go test ./<changed-package>/...
```

`GOWORK=off` matters in this environment -- `/org/go.work` only lists the
primary `./ncli` checkout, not worktree paths, so workspace mode fails to
resolve a worktree's own module.

## Commit conventions

Loosely prefix commit subjects by area when it aids scanning -- `cli:`,
`client:`, `deps:`, `refactor:`, `docs:` are the ones already in use (see
`git log --oneline`). Not a hard rule; clarity over ceremony.

**Never add a `Co-Authored-By: Claude` trailer to any commit in this repo.**

## CHANGELOG.md conventions

This repo doesn't use a literal `## [Unreleased]` placeholder heading in
practice -- the next version number is picked up front and used directly as
the heading (`release.yml` *would* accept `Unreleased` and ask you to rename
it, but the working convention here is to just commit to the number early).

`(#N)` renders as plain text in CHANGELOG.md itself (GitHub only autolinks
issue/PR numbers in "rich" surfaces -- issues, PRs, commits, releases --
not in a tracked file's blob view, verified via `gh api markdown` with repo
context vs. the file's own rendered HTML). It becomes a real link once a
release section is copied into the GitHub Release body, which is the only
place it needs to be one.

## Merging: by hand in the GitHub UI, no squash

Merging a PR is a manual step done through the GitHub UI -- never `gh pr
merge`, even from an agent-driven session. This is a deliberate, temporary
guardrail while the team builds confidence/process maturity around
unattended merges, not a permanent rule -- revisit it once that maturity
exists. An agent may push commits, open/update a PR, and mark it ready for
review, but the merge click itself is a human action for now.

When a human does merge, use **"Rebase and merge"** or **"Create a merge
commit"** -- not squash. Squash-merge is disabled repo-wide
(`allow_squash_merge: false`) so the button isn't there to reach for;
rebase-merge and merge-commit are both still available. (Full linear
history -- i.e. also banning merge commits -- was considered and dropped:
it would leave rebase-merge as the *only* legal method, which is stricter
than this repo actually needs.)

This repo has `deleteBranchOnMerge` off, so branch cleanup after merge
(`git push origin --delete <branch>`) doesn't happen automatically --
always do it by hand.

## `main` branch protection

Applied as of 2026-07-21 (`gh api repos/ohstr/ncli/branches/main/protection`,
verified with a follow-up `GET`):

- Require a pull request before merging, 0 required approvals (single
  maintainer for now -- add a review-count requirement once there's a
  second regular reviewer).
- Require the `check` status check from `.github/workflows/ci.yml` to pass.
- Block force pushes to `main`.
- Block deletion of `main`.
- Require conversation resolution before merging.
- Enforced on admins too, not just other contributors.

## Cutting a release

### Version number

`ncli` is pre-1.0 (`0.y.z`), so semver's initial-development rule applies:
MAJOR doesn't come into play yet. **MINOR** covers anything that isn't a
pure bug fix -- including breaking changes to flags/output (see `0.1.0` →
`0.2.0`, which bumped minor despite the `id delegate` flag rename being
breaking). **PATCH** is reserved for a release that's *only*
backwards-compatible bug fixes, nothing under `### Added`/`### Changed`.

### Fire `.github/workflows/release.yml` by hand

It's `workflow_dispatch` only -- nothing triggers it automatically:

```sh
gh workflow run release.yml                    # uses the topmost CHANGELOG.md heading
gh workflow run release.yml -f version=0.3.0    # or pin it explicitly, no "v" prefix
```

`-f version=` takes the bare number (`0.3.0`), never `v0.3.0` -- the tag
itself gets the `v` prefix added internally (`vX.Y.Z`), but the input and
the `## [X.Y.Z]` CHANGELOG heading it's matched against never have one. It
vets/tests as a gate (so a red `main` doesn't silently ship), resolves
notes from that CHANGELOG.md section verbatim, tags `vX.Y.Z`, and runs
GoReleaser (binaries + checksums, `ghcr.io` image, homebrew tap). Safe to
re-run if GoReleaser fails partway -- the tag step deletes and recreates
`vX.Y.Z` before retrying rather than erroring on "tag already exists".

### Verify it actually published

```sh
gh release view vX.Y.Z
```

Check the tag landed, the binaries/`checksums.txt` are attached, and the
notes match what was reviewed per the generic doc's release-checklist step
1. Spot-check the `ghcr.io` image tag and the homebrew tap formula bump
too if either matters for this release.

## Gotchas learned

- `.agents/` (this directory) is git-tracked on purpose, unlike
  `.claude/skills/`, which is gitignored (`.gitignore:40`) and therefore
  never actually reaches a teammate via `git clone` no matter what's written
  there. If a process doc needs the whole team to have it, it can't live
  under `.claude/`. The org-wide `dev-workflow` doc this file overlays is a
  deliberate exception: it lives in its own repo,
  [`ohstr/dev-workflow`](https://github.com/ohstr/dev-workflow), shared
  across ohstr repos rather than owned by any one of them -- cloned
  locally at `/u/flzpace/xgit/orgs/ohstr/dev-workflow`, sibling to `ncli`.
- `skills/*` (top-level, no leading dot) is a different audience entirely --
  written for agents/users who only have the `ncli` binary, no source tree,
  distributed via the plugin marketplace (`/plugin install ncli-apply@ncli`,
  `npx skills add ohstr/ncli --all -y`). Don't add contributor-process
  content there.
