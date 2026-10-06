# forge-wingman — Claude Code conventions

Repo-specific conventions for working on this codebase. Global conventions
(git, PR workflow, code style) come from `~/.claude`'s layered config; this
file only holds what's specific to *this* repo.

## Default the run-lane to agentic worktrees, not supervise

When `/spec-plan` gates step 2b's run-lane (branch in this directory vs a
separate worktree an agent runs), **default-recommend the separate worktree,
agent-run lane** for this repo specifically. This product's own reason for
existing is autonomous agent-driven builds — dogfooding that path here is the
point, not an exception carved out for convenience.

**Reserve "you supervise" for work that genuinely warrants closer
supervision**, and say so explicitly when recommending it over the default:
a change with real money/budget consequences (e.g. FRG-20's cost-ceiling
logic), or a ticket sequenced ahead of a sibling because they rewrite the
same core function (e.g. FRG-19 before FRG-26, both touching `Build()`'s
phase sequence) — closer review reduces the chance of a collision the
sibling's plan then has to re-ground against.

This is a picker-default change, not a rule removal — still gate per ticket,
just flip which option is recommended.

## Tickets the runner will build

The runner reads only a ticket's title and description, and its build agent reads
this file as project memory, so both rules below reach the run.

- **State invariants, not just mechanisms.** When a ticket gives some calls a thing
  and not others, first name the state those calls share: a review fix round resumes
  the build session, so anything shaping that session's prompt must be identical on
  every round of it. *(Judgment call. Verified 2026-10-06: FRG-50's ticket said fix
  rounds get no rules; the build followed it and its own review flagged the break.)*
- **Making a per-attempt name fixed removes the uniqueness that made each create
  safe.** When a path or branch stops carrying the attempt id, re-check every create
  of it for a retry in the same change: `git worktree prune` then `add -B`, never
  `-b`. *(Judgment call. Verified 2026-10-06 on FRG-50.)*

## Walk agent-built changes in auto mode

When reviewing a change an agent built in this repo, run `/changes-walkthrough` in
**auto** mode first: every file walked in data-flow order, the mechanical checks per
file (mutating the guards in changed lines, caller-to-callee, comment budget, consumer
counts), every local finding fixed and re-verified, and trade-offs batched into one
picker. Recommend `auto` first at the walk's start gate; the file-by-file human read
stays one tap away.

*(Owner's instruction, 2026-10-04.)*

## Merging PRs for the owner

When the owner asks me to merge a PR in this repo, I run `gh pr ready` and
`gh pr merge --squash --delete-branch` myself instead of handing the commands
back. Only when ALL hold:

- the owner has said to merge THAT PR, after seeing what I verified (the
  claims checked against the code, the CI result);
- CI is green on the current head sha, re-read just before;
- the PR is in alvintoh/forge-wingman, never another repo.

This needs a permission rule, because the auto-mode classifier blocks
`gh pr merge` as "merge without review" and CLAUDE.md cannot override it:
allow `Bash(gh pr merge:*)` for this repo only. Without the rule I print the
commands instead.

*(Judgment call, owner-confirmed 2026-10-01: "why keep asking me to do it".)*
