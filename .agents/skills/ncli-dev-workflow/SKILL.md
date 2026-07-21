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
Squash-merge is disabled repo-wide (`allow_squash_merge: false`) so the
button isn't there to reach for; rebase-merge and merge-commit are both
still available. (Full linear history -- i.e. also banning merge commits --
was considered and dropped: it would leave rebase-merge as the *only* legal
method, which is stricter than this step actually needs.)

Immediately after merging, delete the remote branch (`git push origin
--delete <branch>`, or the "Delete branch" button GitHub shows on a merged
PR's page -- this repo has `deleteBranchOnMerge` off, so nothing does this
automatically). Unlike the merge click itself, this is routine cleanup an
agent can do without a human in the loop -- it's trivially recoverable
(the commits live on in `main`'s history via the merge commit either way).
If the branch has a local worktree tied to it (`EnterWorktree`/`git
worktree add`), it's safe to remove too, but ask first or wait to be
asked -- `ExitWorktree` deliberately won't remove one unprompted.

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

### 1. Review CHANGELOG.md's topmost section before touching anything else

This becomes the public GitHub Release notes verbatim (§4 below, "Fire
release.yml") -- read every
bullet under the topmost `## [X.Y.Z]` heading top to bottom and fix, in a
normal commit, anything that fails these checks:

- **Correctness** -- does the bullet still match what actually shipped?
  Flag names, error codes, behavior described in prose can go stale between
  when it was written and release time, especially if a later PR touched
  the same area again. Spot-check anything that looks even slightly off
  against the real diff/`git log`, don't just trust the prose.
- **No sensitive info** -- no real hostnames/URLs, tokens, keys, internal
  infra details, or anything else that isn't meant to be public. Easy to
  miss if a bullet was drafted quickly mid-session and copied from a
  debugging example.
- **No noise** -- only user-facing `ncli` behavior belongs here (see the
  feature/hotfix-vs-trivial and product-vs-process splits in step 1,
  "Decide: feature/hotfix, or trivial?", near the top of this file).
  Internal refactors, test-only changes, or process/doc-only work (like
  this skill file) should never have a bullet. Delete any that snuck in.
- **Every bullet has its `(#N)` PR backreference** (step 4, "Push, open a
  draft PR"). Fill in any that are missing now -- once tagged, the release
  notes are frozen, and that traceability is gone for good if it's absent.
  `(#N)` renders as plain text in CHANGELOG.md itself (GitHub only
  autolinks issue/PR numbers in "rich" surfaces -- issues, PRs, commits,
  releases -- not in a tracked file's blob view, verified via `gh api
  markdown` with repo context vs. the file's own rendered HTML). It
  becomes a real link once this section is copied into the GitHub Release
  body below, which is the only place it needs to be one.

### 2. Confirm the version number

`ncli` is pre-1.0 (`0.y.z`), so semver's initial-development rule applies:
MAJOR doesn't come into play yet. **MINOR** covers anything that isn't a
pure bug fix -- including breaking changes to flags/output (see `0.1.0` →
`0.2.0`, which bumped minor despite the `id delegate` flag rename being
breaking). **PATCH** is reserved for a release that's *only*
backwards-compatible bug fixes, nothing under `### Added`/`### Changed`.

Look at what's actually accumulated under the topmost heading's
subsections and match the number to that verdict -- if the heading was
opened assuming a patch release but a feature landed on top of it later
(common, since the number is picked up front per step 3, "Implement"),
re-bump it now via a normal commit before shipping. Don't let a stale
number ship just because it was the first guess.

### 3. Merge everything intended for this release

(Manual UI merge, per step 5, "Merging: by hand in the GitHub UI, no
squash", above -- nothing new here, just the checkpoint
to confirm everything intended for this cycle actually landed on `main`
before firing the workflow.)

### 4. Fire `.github/workflows/release.yml` by hand

It's `workflow_dispatch` only -- nothing triggers it automatically:

```sh
gh workflow run release.yml                    # uses the topmost CHANGELOG.md heading
gh workflow run release.yml -f version=0.3.0    # or pin it explicitly, no "v" prefix
```

`-f version=` takes the bare number (`0.3.0`), never `v0.3.0` -- the tag
itself gets the `v` prefix added internally (`vX.Y.Z`), but the input and
the `## [X.Y.Z]` CHANGELOG heading it's matched against never have one. It
vets/tests as a gate (so a red `main` doesn't silently ship), resolves
notes from that CHANGELOG.md section verbatim (why the review in §1 above
and each bullet's `(#N)` PR backreference from step 4, "Push, open a draft
PR, iterate", matter), tags `vX.Y.Z`, and runs
GoReleaser (binaries + checksums, `ghcr.io` image, homebrew tap). Safe to
re-run if GoReleaser fails partway -- the tag step deletes and recreates
`vX.Y.Z` before retrying rather than erroring on "tag already exists".

### 5. Verify it actually published

```sh
gh release view vX.Y.Z
```

Check the tag landed, the binaries/`checksums.txt` are attached, and the
notes match what was reviewed in §1 above. Spot-check the `ghcr.io` image
tag and the homebrew tap formula bump too if either matters for this
release.

### 6. Clean up any stragglers

Every PR that went into this release should already have had its branch
(and worktree, if any) cleaned up immediately after merging, per step 5,
"Merging: by hand in the GitHub UI, no squash", above. Use the release as
a checkpoint to sweep anything that was missed:

```sh
git branch -r --merged origin/main | grep -v 'origin/main$'
```

Nothing else needs to happen after that -- the next feature/hotfix branch
opens a fresh `## [X.Y.Z]` heading (step 3, "Implement") when it lands its
first entry.

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
