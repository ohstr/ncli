---
name: ncli-dev-workflow
description: The contribution/release workflow this repo (ohstr/ncli) itself follows -- worktree-first development, when a GitHub tracking issue is required vs. skipped, atomic commit conventions, keeping CHANGELOG.md current as you go, draft-PR mechanics, and how a release actually gets cut. Use before starting any change to this source tree (not the ncli binary's own commands -- see skills/ for those), and always before committing, pushing, or opening a PR against ohstr/ncli.
---

<!-- Process doc, not a command reference. Update by hand when the workflow
changes -- nothing here is derived automatically from the repo. -->

# Contributing to ncli

This governs work *on* ncli itself: worktrees, issues, commits, PRs,
CHANGELOG.md, and releases. It assumes git/gh access to this source tree --
unlike `skills/*` (binary-only, written for agents/users who only have the
`ncli` binary on PATH) and `.claude/skills/verify` (local build/run
verification, not process).

`main` is protected -- nothing lands there except through a merged PR. See
"`main` branch protection" below for the live ruleset.

## 0. Always start in a worktree

When driven by Claude Code, never edit files directly in the primary
checkout. Use the `EnterWorktree` tool (or `git worktree add` by hand) before
the first edit. This is what keeps parallel sessions/agents from clobbering
each other's in-progress work in the same tree.

## 1. Decide: feature/hotfix, or trivial?

This fork in the road decides whether a tracking issue is needed. Everything
still goes through a PR either way -- `main` doesn't take direct pushes.

**Feature/hotfix** (needs an issue first) -- anything that changes `ncli`'s
behavior, adds a capability, fixes a real bug, or is itself a repo-process/
policy decision worth a durable record (this file is an example of the
latter). Product-behavior changes in this bucket also earn a CHANGELOG.md
entry; process-only changes don't -- CHANGELOG.md is scoped to `ncli`'s own
behavior, not contributor process, so "needs an issue" and "earns a
CHANGELOG line" are related but not the same test.

**Trivial** (no issue, straight to a PR) -- typo fixes, comment-only edits,
asset/image swaps (e.g. a `docs/vhs/*.gif`), formatting-only changes, a
dependency bump with no behavior to call out.

When in doubt, treat it as a feature/hotfix -- the issue is cheap, and it's
the one artifact that survives across sessions when a Claude Code
conversation doesn't.

## 2. Feature/hotfix: open the tracking issue first

Before writing code, not after:

```sh
gh issue create --title "id delegate: resolve issuers from vault identities" \
  --body "What/why. This is the spec an agent or reviewer picks up cold."
```

The issue is the durable rationale -- what CHANGELOG.md prose explains
*after the fact*, the issue can capture *before* the fact (constraints,
rejected alternatives, discussion). Note the issue number; the PR opened in
step 4 closes it.

## 3. Implement: atomic commits, CHANGELOG as you go

Split commits by concern, not by file-touch-order -- each commit should be
independently buildable and reviewable in isolation. Verify that with a
stash/build/pop cycle between commits if you're not sure:

```sh
git stash push -u -m "rest of the wip"
GOWORK=off go build ./... && GOWORK=off go test ./<changed-package>/...
git stash pop
```

(`GOWORK=off` matters in this environment -- `/org/go.work` only lists the
primary `./ncli` checkout, not worktree paths, so workspace mode fails to
resolve a worktree's own module.)

Loosely prefix commit subjects by area when it aids scanning -- `cli:`,
`client:`, `deps:`, `refactor:`, `docs:` are the ones already in use (see
`git log --oneline`). Not a hard rule; clarity over ceremony.

**Never add a `Co-Authored-By: Claude` trailer to any commit in this repo.**

Update `CHANGELOG.md` in the same commit as the behavior it describes, not
as an afterthought at the end. Add your entry under the **topmost** `##
[X.Y.Z]` heading, in the right subsection (`### Added`/`### Changed`/`###
Fixed`):

- If the topmost heading is the version you're shipping into, just append.
- If the last release just shipped and nobody's opened the next heading yet,
  add a new `## [X.Y.Z]` heading yourself, picking the semver bump
  (patch/minor/major) the change actually warrants.

This repo doesn't use a literal `## [Unreleased]` placeholder heading in
practice -- the next version number is picked up front and used directly as
the heading (`release.yml` *would* accept `Unreleased` and ask you to rename
it, but the working convention here is to just commit to the number early).

## 4. Push, open a draft PR, iterate

```sh
git push -u origin <branch>
gh pr create --draft --title "..." --body "Closes #<issue-number>

## Summary
...

## Test plan
- [ ] ..."
```

Keep pushing commits (same splitting discipline) until it's done, then mark
the PR ready for review. `Closes #N` in the PR body is what links the issue
-- GitHub closes it automatically on merge and cross-links both directions,
so the CHANGELOG doesn't need to separately mention the issue number.

**Once the PR number is known** (right after the first push above), go back
and append `(#N)` to the CHANGELOG.md bullet(s) this PR introduces, in a
small follow-up commit before merging. This is what gets a tracker reference
into the release notes later -- `release.yml` copies CHANGELOG.md sections
verbatim, so if the PR number isn't in the text, it's not in the release.
Use the **PR** number here, not the issue number: every change has a PR,
not every change has an issue, so PR-number is the one reference that works
uniformly for both the feature/hotfix and trivial paths.

## 5. Merging: by hand in the GitHub UI, no squash

Merging a PR is a manual step done through the GitHub UI -- never `gh pr
merge`, even from an agent-driven session. This is a deliberate, temporary
guardrail while the team builds confidence/process maturity around
unattended merges, not a permanent rule -- revisit it once that maturity
exists. An agent may push commits, open/update a PR, and mark it ready for
review, but the merge click itself is a human action for now.

When a human does merge, use **"Rebase and merge"** or **"Create a merge
commit"** -- not squash. Squashing collapses the atomic commit history from
step 3 back into one commit on `main`, defeating the point of splitting it.
Squash-merge is
disabled repo-wide (`allow_squash_merge: false`) so the button isn't there
to reach for; rebase-merge and merge-commit are both still linear-history-
compatible enough for our purposes and remain available. (Full linear
history -- i.e. also banning merge commits -- was considered and dropped:
it would leave rebase-merge as the *only* legal method, which is stricter
than this step actually needs.)

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

1. Confirm the topmost `## [X.Y.Z]` heading in `CHANGELOG.md` is the version
   actually being shipped (rename/re-bump via a normal PR if not).
2. Merge everything intended for this release.
3. Fire `.github/workflows/release.yml` by hand -- it's `workflow_dispatch`
   only, nothing triggers it automatically:

   ```sh
   gh workflow run release.yml                    # uses the topmost CHANGELOG.md heading
   gh workflow run release.yml -f version=0.3.0    # or pin it explicitly
   ```

   It vets/tests, resolves notes from that CHANGELOG.md section verbatim
   (which is why step 4's `(#N)` references matter -- they're what makes the
   release notes point back at the PRs that shipped it), tags `vX.Y.Z`, and
   runs GoReleaser (binaries, `ghcr.io` image, homebrew tap).
4. Nothing needs to happen after -- the next feature/hotfix branch opens a
   fresh `## [X.Y.Z]` heading per step 3 when it lands its first entry.

## Gotchas learned

- `.agents/` (this directory) is git-tracked on purpose, unlike
  `.claude/skills/`, which is gitignored (`.gitignore:40`) and therefore
  never actually reaches a teammate via `git clone` no matter what's written
  there. If a process doc needs the whole team to have it, it can't live
  under `.claude/`.
- `skills/*` (top-level, no leading dot) is a different audience entirely --
  written for agents/users who only have the `ncli` binary, no source tree,
  distributed via the plugin marketplace (`/plugin install ncli-apply@ncli`,
  `npx skills add ohstr/ncli --all -y`). Don't add contributor-process
  content there.
