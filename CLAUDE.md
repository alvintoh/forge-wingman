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

## Work-in-progress limits

At most **5 tickets in progress in the interactive lane** (you supervise, in
this directory or a worktree you drive) and **5 in the agentic lane**. Before
`/spec-plan` starts another interactive ticket, count the owner's In Progress
tickets in Linear; at 5, finish or park one first. The agentic 5 is the
dispatcher's `ReviewWIP` (open agent PRs, `internal/dispatcher/admit.go:53`),
so a run can still start beside four open PRs. The live value is the
`WINGMAN_REVIEW_WIP` key in `dispatcher_settings`; the code default is 3.

**The agentic limit started at 3 as a trial and was raised to 5 (set
2026-10-12)** (a judgment call: the owner preferred a high start and will
observe). Go higher
only when both hold: model quota is left over at the end of a typical day, and
quality holds at 5 (agent PRs merge without extra fix rounds or owner rework).
Propose the raise with that evidence; never raise it on throughput alone.

*(Owner's instruction, 2026-10-08; raised to 5, set 2026-10-12.)*

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

## Front-end PRs: design first, screenshots last

A PR held for the owner as a front-end change (`cmd/surface/`, `web/`) is walked in this order:
the owner finalizes the design, and may change it, so ask "is the design final?" before any
sweep; then `/fe-sweep` on the final design; then `/pr-review`; then, after its fix commits,
capture the s, m, l, xl Chromium shots of each state the PR touches at the final head (s is the
current iPhone Pro Max, Playwright's `iPhone 17 Pro Max`, 440x763 at scale 3; then 768x1024, 1024x768, 1280x800; every band at its full viewport, uncropped unless the state is one modal or card) and attach them to the PR body as one table. An edit after capture invalidates
the shots, so the capture is the last step before merge. This is the product's own flow for the
repositories it builds in.

*(Judgment call, owner's instruction 2026-10-15, superseding 2026-10-13's "sweep first, review
second"; no authority prescribes the order.)*

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

## omp is the default harness, and a fallback needs evidence

A plan's harness is **omp** unless omp cannot reach the model. Establish that by
reading omp's own provider registry (the provider id, and the `envVars` it reads)
and then running the model *through omp* — never from our adapter's map, which
lists only what a plan has been given: it had one entry, so the
Gemini plan defaulted to opencode, although omp's built-in `google` provider
drives that model natively (`google-generative-ai`) on the same `GEMINI_API_KEY`
the repository secret already uses.

A fallback that cannot serve the model is worse than none — it reads as coverage
while covering nothing, and has to be maintained as if it worked. opencode's
OpenAI-compatible path cannot complete a tool call against Gemini at all: a
thinking model returns a `thought_signature` with every call, which that layer
never echoes, so each second turn fails.

*(Owner's instruction, 2026-10-11; verified live on gemini-3.8-flash through omp
and through opencode the same day.)*
