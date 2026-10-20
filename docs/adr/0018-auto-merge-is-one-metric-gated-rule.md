# 0018 — Auto-merge is one metric-gated rule with an owner switch

- **Date:** 2026-10-17
- **Status:** accepted — the switch, the rule and the removal of the levels are live (2026-10-19); the daily run cap was dropped before it shipped (see the struck bullet under Decision)
- **Updated (2026-10-19):** the product no longer delegates a ticket to itself, and the daily run cap is gone (see the struck bullet under Decision).

## Context

The PRD's autonomy ladder has six levels, two rewrite-rate bars (5% to advance, 10% to roll back), a 20% ceiling and four extra clauses (a delivery step exists, no gate misses, all PRs measured, not rolled back). The levels also set how many runs start per day and switch transient requeue and self-delegation on. For one owner it is more machinery than the decision it serves, which is binary: does this PR merge itself, or do I merge it. The owner said so: "i feel i am overengineering it so i am trying to make it simple".

## Decision

**Auto-merge is on when the trailing-10 rewrite rate over ten measured PRs is 10% or less and none of them is an auto-merge that was reverted; otherwise it is off.**

- **Per-PR eligibility is unchanged:** size S and M only (FR-16), a front-end change is held, a PR in the 20% sample is held (FR-17).
- **An owner switch, on by default.** Turning it off makes every PR manual whatever the rate. It lives in Firestore beside the peak-mode setting and is set by a command (`runner auto-merge`) and by a "..." button beside the status in the inbox and the Autonomy page that opens a small settings modal: an on/off switch with the rate shown, applied by SAVE and discarded by BACK, so a change needs no redeploy. The page shows two separate facts in words: the switch state (the owner's setting) and the rate line (whether the rate allows auto-merge); the effective state needs both. Setting it from the page is a write from the surface to that one Firestore document, so the surface's service account gets write access to it and nothing else. No document means on; a switch that cannot be read holds (off) and logs.
- **Retired:** levels 0 to 5, the 5% bar, the 20% ceiling, the "delivery step exists" and gate-miss clauses.
- ~~**Replaced by plain settings:** the daily run cap is one configured number (`WINGMAN_DAILY_RUNS`, default 5); transient requeue and self-delegation are always on.~~
  **Superseded (2026-10-19):** the product never starts work by itself. Work starts only when the owner delegates a ticket (by hand in Linear, through `/drive-phase`, or by the surface's START PHASE), so there is no self-delegation and no daily run cap; transient requeue stays always on. The owner's words: "i dont think forge-wingman should run automatically by itself". Self-delegation was built (FRG-125, FRG-164) and, the day it went live, rolled back and removed (FRG-181). The cap bounded nothing once self-delegation was gone, since the owner's own delegations never counted against it; budget ceilings, review WIP, the adaptive limit and the per-repo rule still bound the work.
- **The rate is the measure.** A rate above 10% turns auto-merge off by itself, and the 10% is the same bar as the Phase 3 exit gate.

## Known limitation and revisit trigger

An auto-merged PR carries no owner edits, so once auto-merge is on those PRs read as a 0% rewrite and dilute the rate; a drop in review quality is then seen later than it would be. The row's `auto_merge` field means the PR was labelled eligible, not that the bot merged it, so it cannot be used to leave them out. **Revisit when the first PRs auto-merge:** record who merged each PR at measurement (GitHub's single-PR `merged_by`), leave bot merges out of the rate, and read the 20% sample (FR-17) as the unbiased check. Shipping the simple rule first is deliberate: the owner wants it in production and tuned from data.

## Alternatives

| # | Option | Why not |
|---|--------|---------|
| 1 | keep the ladder and the two bars | six levels and four clauses for a binary decision; hysteresis between 5% and 10% is a judgment call the owner does not want to maintain |
| 2 | one 10% bar, keep the levels underneath | the rule is simple but the machinery still has to be explained and tested |

## Consequences

- **A gate on a quality metric with automatic fallback to manual is the shape of an error-budget policy** (SRE practice: ship while within budget, a person decides once it is spent). That analogy is a judgment call, not a standard that names this case.
- **Shipping the rule turns auto-merge on at the live 9.8%.** The owner accepted that. The switch ships first, so it can already be turned off.
- `adr/0017` and the inbox no longer show a level; the Autonomy page shows the status, the rate and the reason.
- `adr/0017`'s `wait_reason` value is `auto-merge-off`.
