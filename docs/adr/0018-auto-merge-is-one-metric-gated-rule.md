# 0018 — Auto-merge is one metric-gated rule with an owner switch

- **Date:** 2026-10-17
- **Status:** accepted — the switch, the rule and the removal of the levels are live (2026-10-19); the daily run cap was dropped before it shipped (see the struck bullet under Decision)
- **Updated (2026-10-19):** the product no longer delegates a ticket to itself, and the daily run cap is gone (see the struck bullet under Decision).
- **Updated (2026-10-31):** the rate is a guide, not a gate: the owner's switch alone decides whether auto-merge is on (see the struck lines under Decision). The owner: "i thought its controlled entirely by the configuration ? the 10% rewrite is just a guide".
- **Updated (2026-11-10):** the rate gates again, under the switch. Auto-merge is on only while the switch is on *and* the trailing-10 rate over the PRs a human merged is at or under the bar (20% unless the owner sets another). The rate is no longer diluted by auto-merged PRs: a PR that merged itself is left out of the window. An auto-merged PR that a later merge reverted or named as fixed within 7 days latches auto-merge off until the owner switches it on again.

## Context

The PRD's autonomy ladder has six levels, two rewrite-rate bars (5% to advance, 10% to roll back), a 20% ceiling and four extra clauses (a delivery step exists, no gate misses, all PRs measured, not rolled back). The levels also set how many runs start per day and switch transient requeue and self-delegation on. For one owner it is more machinery than the decision it serves, which is binary: does this PR merge itself, or do I merge it. The owner said so: "i feel i am overengineering it so i am trying to make it simple".

## Decision

~~**Auto-merge is on when the trailing-10 rewrite rate over ten measured PRs is 10% or less and none of them is an auto-merge that was reverted; otherwise it is off.**~~

**Superseded (2026-10-31): auto-merge is on when the owner's switch is on, and off when it is off.** The trailing-10 rewrite rate is shown against a 10% guide and a reverted auto-merge in that window is shown too; neither gates anything. The reasons the old rule computed (`too-few-measured`, `no-agent-lines`, `rate-above-bar`, `reverted`) stay as information on the Autonomy page. A switch that cannot be read still holds (off).

**Updated (2026-11-10): the rate gates again, and the switch stays the master control.** Auto-merge is on only while the owner's switch is on and the trailing-10 rewrite rate is at or under the bar; exactly at the bar allows it. Above the bar, and wherever the rate cannot be computed — fewer than ten PRs a human merged are measured, the window carries no agent lines, or the read fails — every runner PR waits for the owner, and its wait reason and PR body name the rate and the bar, or which of those conditions stopped it being computed. The rate is taken over merged runner PRs a **human** merged: a PR that merged itself is left out of the window, as is a row measured before the merge account was kept, because an unknown account is neither the owner's reading nor proof of a self-merge. Neither lowers the rate or counts as one of the ten, so a 0% rewrite on a PR nobody read can no longer hide a defect, and after this change auto-merge holds off until ten known human merges are measured. The row records GitHub's `merged_by` login at measurement for that. **The bar is a setting:** it lives beside the switch in `dispatch/auto-merge`, defaults to 20%, and is set with `runner auto-merge -bar <percent>`; a stored value that is not a number from 1 to 100, or a settings read that fails, holds auto-merge off and names which. **An escaped PR latches it off:** an auto-merged PR a later merge reverted, or named as fixed (`fixes #<n>`), within 7 days of its merge holds auto-merge off until the owner switches it on again with `runner auto-merge -mode on`; every runner PR's wait reason names the escaped PR. Switching on **acknowledges** the PR the latch named, which the document remembers (`cleared_escapes`): the same revert or fix is read again on every poll and must not latch it a second time, while an escape of another PR still does. A revert of a PR measured before the merge account was kept is told by GitHub's own record of the merge, then by the row's eligibility mark, so an unproven account latches rather than passes. `pr-review-post` reads the switch and the latch again just before it requests the merge and declines when either holds or cannot be read, since the eligibility label was set before the review ran. The rate alone cannot see a defect in a PR nobody read, which is what the latch is for.

A size-S run is now eligible without a recorded plan, because an S ticket has no plan stage: the absence of a plan holds nothing for S, while a size-M or L run still waits without one.

- **Per-PR eligibility is unchanged:** size S and M only (FR-16), a front-end change is held, a PR in the 20% sample is held (FR-17).
- **An owner switch, on by default.** Turning it off makes every PR manual whatever the rate. It lives in Firestore beside the peak-mode setting and is set by a command (`runner auto-merge`) and by a "..." button beside the status in the inbox and the Autonomy page that opens a small settings modal: an on/off switch with the rate shown, applied by SAVE and discarded by BACK, so a change needs no redeploy. The page shows two separate facts in words: the switch state (the owner's setting) and the rate line (~~whether the rate allows auto-merge); the effective state needs both~~ the rate against its 10% guide). **Updated (2026-10-31):** the effective state is the switch alone. Setting it from the page is a write from the surface to that one Firestore document, so the surface's service account gets write access to it and nothing else. No document means on; a switch that cannot be read holds (off) and logs.
- **Retired:** levels 0 to 5, the 5% bar, the 20% ceiling, the "delivery step exists" and gate-miss clauses.
- ~~**Replaced by plain settings:** the daily run cap is one configured number (`WINGMAN_DAILY_RUNS`, default 5); transient requeue and self-delegation are always on.~~
  **Superseded (2026-10-19):** the product never starts work by itself. Work starts only when the owner delegates a ticket (by hand in Linear, through `/drive-phase`, or by the surface's START PHASE), so there is no self-delegation and no daily run cap; transient requeue stays always on. The owner's words: "i dont think forge-wingman should run automatically by itself". Self-delegation was built (FRG-125, FRG-164) and, the day it went live, rolled back and removed (FRG-181). The cap bounded nothing once self-delegation was gone, since the owner's own delegations never counted against it; budget ceilings, review WIP, the adaptive limit and the per-repo rule still bound the work.
- ~~**The rate is the measure.** A rate above 10% turns auto-merge off by itself, and the 10% is the same bar as the Phase 3 exit gate.~~
  **Superseded (2026-10-31): the rate is the guide.** A rate above 10% is shown as above the guide and turns nothing off; the owner reads it and decides with the switch. The 10% is still the Phase 3 exit gate's bar. Consequence: auto-merge can be on while the rate is above the guide, so the rewrite rate can rise unchecked until the owner switches it off. The sampled 20% (FR-17) and the front-end hold are unchanged.

## Known limitation and revisit trigger

~~An auto-merged PR carries no owner edits, so once auto-merge is on those PRs read as a 0% rewrite and dilute the rate; a drop in review quality is then seen later than it would be. The row's `auto_merge` field means the PR was labelled eligible, not that the bot merged it, so it cannot be used to leave them out. **Revisit when the first PRs auto-merge:** record who merged each PR at measurement (GitHub's single-PR `merged_by`), leave bot merges out of the rate, and read the 20% sample (FR-17) as the unbiased check.~~

**Addressed (2026-11-10):** the measurement records GitHub's `merged_by` login, and the rate window leaves out every PR that merged itself. The rate still cannot see a defect in a PR nobody read, which is why the escape latch (a revert or a `fixes #<n>` within 7 days) holds auto-merge off until the owner switches it on again. The 20% sample (FR-17) stays the unbiased check.

**Accepted (2026-11-11): a request already made is not withdrawn by a later switch-off or latch.** `pr-review-post` re-reads the switch and the latch just before it requests the merge, but once GitHub holds the request only a push to the branch withdraws it (`automerge-withdraw.yml`), so the window is the PR's remaining CI time. The rate and the bar are checked at pr-meta, not again at the request. Revisit if an auto-merge ever lands inside that window after the owner switched it off.

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
