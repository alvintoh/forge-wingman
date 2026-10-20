# forge-wingman — Claude Code conventions

Repo-specific conventions for working on this codebase. Global conventions
(git, PR workflow, code style) come from `~/.claude`'s layered config; this
file only holds what's specific to *this* repo.

## The harness builds by default; delegating to forge-wingman is an option

When a Claude Code or omp session takes a ticket or a phase (`/spec-plan`,
`/dev-build`, `/drive-phase`), the default is that this harness builds it: a
worktree off `main`, builders dispatched by `/dev-build`, and the owner reviews
and merges. Delegating to forge-wingman's runner (the Linear delegate, the plan
in the description) is offered at the same gate as an option, never as the
recommendation. Recommend it only when asked, or when the session is ending and
the ticket should keep moving without it.

*Superseded (2026-10-20): ~~default-recommend the separate worktree, agent-run
lane, because dogfooding the runner is the point~~. Owner's instruction: "when i
open claude code, or omp in interactive mode i want that harness to run
development tasks by itself, whereas there should be an option to delegate to
forge-wingman but its not the recommended". A judgment call.*

## Work-in-progress limits

At most **5 tickets in progress in the interactive lane** (you supervise, in
this directory or a worktree you drive) and **5 in the agentic lane**. Before
`/spec-plan` starts another interactive ticket, count the owner's In Progress
tickets in Linear that are **not delegated to the runner**: a run's ticket shows
In Progress from the claim that starts it, and the agentic lane already bounds it
through the dispatcher's review WIP. At 5, finish or park one first. The agentic
5 is the dispatcher's `ReviewWIP` (open agent PRs,
`internal/dispatcher/admit.go:53`), so a run can still start beside four open
PRs. The live value is the `WINGMAN_REVIEW_WIP` key in `dispatcher_settings`;
the code default is 3.

**The agentic limit started at 3 as a trial and was raised to 5 (set
2026-10-12)** (a judgment call: the owner preferred a high start and will
observe). Go higher
only when both hold: model quota is left over at the end of a typical day, and
quality holds at 5 (agent PRs merge without extra fix rounds or owner rework).
Propose the raise with that evidence; never raise it on throughput alone.

*(Owner's instruction, 2026-10-08; raised to 5, set 2026-10-12.)*

## Runs per repo: when to raise it

The per-repo cap (Config settings, FRG-175) starts at 5. Raise it one step at a time, toward 8
and no higher without a new decision, only when both hold: no more than 1 of the last 10 runs at
the cap stopped on `rebase-conflict`, and the review WIP limit is at least as high as the cap,
since more runs than reviewable PRs only builds a queue. Lower it after two rebase-conflict stops
in a day. The platform limit (20) is GitHub's Free-plan job cap and is not ours to raise.

*(Judgment call, owner's instruction 2026-10-19. Basis: of 59 runs so far, 1 stopped on
rebase-conflict and at most 2 ran at once, so 5 and 8 are untested.)*

## Tickets the runner will build

The runner reads only a ticket's title and description, and its build agent reads
this file as project memory, so the rules below reach the run.

- **State invariants, not just mechanisms.** When a ticket gives some calls a thing
  and not others, first name the state those calls share: a review fix round resumes
  the build session, so anything shaping that session's prompt must be identical on
  every round of it. *(Judgment call. Verified 2026-10-06: FRG-50's ticket said fix
  rounds get no rules; the build followed it and its own review flagged the break.)*
- **Making a per-attempt name fixed removes the uniqueness that made each create
  safe.** When a path or branch stops carrying the attempt id, re-check every create
  of it for a retry in the same change: `git worktree prune` then `add -B`, never
  `-b`. *(Judgment call. Verified 2026-10-06 on FRG-50.)*

- **A guard added in review that the ticket does not state goes into the ticket's acceptance
  criteria in the same change.** The Wingman review judges the code against the ticket text, so an
  unstated guard is flagged as scope the ticket never asked for; label the edit `Updated (<date>)`
  or `Added (<date>)` in the ticket. *(Judgment call. Verified 2026-10-15 on FRG-125: a state
  guard and an admission hold added after a mutation review were flagged until AC2 and a new AC6
  named them.)*

- **A plan for an agent-lane ticket goes into the description, never a comment.** The run's
  `ticket.json` comes from Linear's description (`internal/dispatcher/linear.go`) and comments are
  never fetched, so a plan posted as a comment shapes nothing — write it into the description, and
  delegate by setting Linear's `delegate` to the agent id the job acts for (`LINEAR_DELEGATE` on
  `forge-wingman-dispatcher`). *(Judgment call. Verified 2026-10-15 on FRG-135.)*

## Fixing a runner PR: add commits, never rewrite the agent's

Fix a runner PR with new commits on top, each with `Rework: review-found` or `Rework: owner-change`
in its body. A rebase keeps each commit's author and is safe. Never squash or re-author the agent's
commits: the rewrite rate counts a commit as the agent's only when GitHub's author login for it is the
runner's, so a squash turns the agent's lines into owner lines. *(Judgment call from a measured effect.
Verified 2026-10-15: squashing the runner's commits on PR 141 made the first live poll read 0 agent
lines and 4,715 owner lines for it, and the trailing-10 rate 110.4%.)*

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
sweep; then `/fe-sweep` on the final design; then `/fe-evidence`, which captures the s, m, l, xl
Chromium shots of each state the PR touches at that head (s is the current iPhone Pro Max,
Playwright's `iPhone 17 Pro Max`, 440x763 at scale 3; then 768x1024, 1024x768, 1280x800; every band
at its full viewport, uncropped unless the state is one modal or card) and attaches them to the PR
body as one table; then `/pr-review` last, since its fix commits are the only remaining edit and a
fix that changes rendering means re-capturing. This is the product's own flow for the
repositories it builds in.

Before merging any PR that changed a document the vault also holds (`docs/`), run
`/sync-docs forge-wingman` from forge-vault. The vault is the source and the repo's copies
drift: on 2026-10-17 four documents differed after runner PRs edited their repo copies. A
drawing is synced at its own end, by forge-vault's `/proto-draw`.

*(Judgment call, owner's instruction 2026-10-15, superseding 2026-10-13's "sweep first, review
second" and the same day's "review before evidence"; no authority prescribes the order. The
`/sync-docs` step is the owner's instruction of 2026-10-17.)*

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

## Concurrency and parallel work

**Five in flight is this repository's ceiling, and it is not a quota.** Issue up to five
independent tool calls or subagent spawns at once rather than waiting on each, and
when one finishes start the next without being asked. Serialising independent work
is a defect to fix, not a safe default. Idle only on a real dependency (a result
you need), an outward action the owner owns, or a gate whose answer is genuinely
theirs.

**This holds whatever harness a run uses, because it is a rule and not a setting.**
omp exposes no agent, task or tool concurrency limit — the numbers that look like
one govern background jobs (`async.maxJobs`), a judge/completion worker pool, and
the `task` tool's own subagent queue. Read the registry of whichever harness a plan
names and expect the same, so a fallback harness needs no extra configuration. The
only serialisation is per tool: a tool declaring itself exclusive runs alone, and
omp's `edit` and `todo` do because they mutate shared state, so a batch of edits
serialises while reads, shell calls and subagent spawns run together.

*(Judgment call, owner's instruction 2026-10-18: five is a chosen working number,
not a harness limit. Verified against omp 18.7.0's `omp config list` and its
`dist/cli.js`.)*
