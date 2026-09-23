# 0002 — The runner runs on GitHub Actions, per target repository

- **Date:** 2026-09-21
- **Status:** accepted
- **Scope:** the **runner** only. The dispatcher and the surface stay on Cloud Run.

## Context

`prd-v1.md`'s constraints record *"free tier only, on GCP"*, with Cloud Run's
vCPU-second allowance sized at *"~100 runs of 30 minutes"*. That sizing was
computed on vCPU alone, and it omits the dimension that actually binds.

**Cloud Run has no disk.** Google's container contract: *"It is an in-memory file
system, so writing to it uses the instance's memory"*, and *"You can potentially
use up all the memory allocated to your instance by writing to the in-memory file
system, which will crash the instance"* (verified 2026-09-21).

An agentic coding run is the most disk-bound workload the product has: clone the
repo, create the worktree FR-2 requires, resolve dependencies, run the target's
build. On Cloud Run every byte of that is RAM, competing with the agent process for
one budget — and the failure mode is an instance crash mid-run, which FR-6 would
record as a build failure and FR-13 would then *escalate a model tier* for.

The escapes do not work. Cloud Storage FUSE is a network filesystem whose rename
semantics git fights; Filestore carries a floor far above NFR-1's entire ceiling.

**This is a failed precondition, not a better tool.** Cloud Run Jobs remains right
for the dispatcher — scheduled, stateless, CPU-trivial, and its task timeout runs to
168 hours against a service's 60 minutes.

## Decision

**The runner is a Go binary invoked by a GitHub Actions reusable workflow, and the
workflow lives in each target repository rather than centrally.**

A standard runner is an ephemeral VM: 2 vCPU / 8 GB RAM / **14 GB SSD** on a private
repository, 4 vCPU / 16 GB on a public one, with a 6-hour job ceiling against
~30-minute runs (verified 2026-09-21).

**Per-repository, for three independent reasons — any one of them sufficient:**

1. **Log exposure.** A public repository's Actions logs are world-readable. A
   central runner in this product's repo would check out private repositories and
   put their source into those logs.
2. **Billing.** Public repositories get unlimited free minutes; private ones meter
   against 2,000/month. Per-repo, each repository's own visibility governs its own
   minutes instead of one choice governing all of them.
3. **Acceptable use.** GitHub's policy: Actions *"should not be used for activities
   unrelated to the production, testing, deployment, or publication of the software
   project associated with the repository where GitHub Actions are used"* (verified
   2026-09-21). A workflow in repo X that builds a change to X and opens a PR on X is
   squarely the sanctioned use; a central runner modifying other repositories is not.

## Consequences

- **FR-2's isolation is free.** A fresh VM per job means no two runs can share a
  worktree or a branch, with nothing built to guarantee it.
- **FR-12 gets stronger.** The requirement exists because `gh` defaults to a work
  account on the owner's machine. A fresh runner has no ambient `gh` config, so that
  class of mistake cannot occur — the assertion stays, now as defence in depth.
- **FR-5 becomes structural.** Scoping `GITHUB_TOKEN` *withholds* the ability to
  mark a PR ready or merge, rather than denying it by configuration.
- **A second platform in the estate**, and a departure from the GCP-only constraint.
  `prd-v1.md`'s NFR-1 was amended the same day to state that runner minutes count
  toward the cash ceiling.
- **The 14 GB disk is the new ceiling**, and it is a real one for a large dependency
  tree. Shallow clones and `actions/cache`; fail loudly rather than part-way.
- **Public repositories double the runner** (4 vCPU / 16 GB) as well as removing the
  metering — so repository visibility is now a performance decision too.
  ⚠️ **VISIBILITY IS A CONTENT DECISION AND THE COST FOLLOWS IT — never the
  reverse.** *(Corrected 2026-09-21, hours after a first version of this bullet
  called public "the default to prefer", which reads as a nudge to publish in order
  to save money.)* **This record's own first argument is why:** a public
  repository's Actions logs are world-readable, so a public target publishes every
  run's diffs, file contents and error output — a larger surface than the source.
  And `parallax` holds compensation targets and employer assessments, so it can
  never be public whatever the runner costs.

  **The usable form:** where a repo is already public, or would be published on its
  own merits anyway, the runner is free — worth knowing when choosing what to point
  this at FIRST. Where it cannot be, the minutes are a real cost and FR-22 enforcing
  them is the answer. NFR-5's allowlist already carries the classification.
  **`workflow_dispatch` is not a schedule, so the 60-day scheduled-workflow disable
  does not apply** to a public target here.
- ⚠️ **The free tier is EXHAUSTED BELOW the design volume on private repos, and
  this record previously named the limit without its headroom** *(added 2026-09-21
  by a `/tech-design` re-run applying §3b's over-limit rule)*.

  | | Private target | Public target |
  |---|---|---|
  | free allowance | **2,000 min/month** | unmetered |
  | rate beyond it | **$0.006/min**, Linux 2-core | — |
  | at 30 min/run | **66 runs free** | unlimited |
  | **over the limit** | ⚠️ **"usage is blocked once you use up your quota"** with no payment method on file — the runner **stops mid-month**; with a card it bills past NFR-1 instead | — |

  *All figures verified against GitHub's billing documentation, 2026-09-21.*

  **The break-even, which is the number to design against:** NFR-1 allows $20/month
  and the subscription takes $10, leaving ~1,667 paid minutes — so **~121 runs/month
  on private repos at 30 minutes each**, or **~81 at 45 minutes**. Past that, either
  the ceiling breaks or the runner stops.

- ⚠️ **The binding limit is coupled to the least certain number in the product.**
  Run duration is assumed ~30 minutes and has **never been measured** — it is on the
  open list — yet it is the divisor in every figure above. NFR-1's own per-run cap is
  **60 minutes**, at which the free tier is **33 runs**. Measure it before trusting
  any of this.

- ⚠️ **Run VOLUME is unresolved and is deliberately recorded as a range.** This
  record and `prd-v1`'s constraints say ~100 runs/month; `adr/0004` says ~300. They
  are not reconciled here because **the answer depends on measurement that has not
  happened** — the three-trap probe and the first real runs settle both duration and
  throughput. **Until then the break-even above is the operative number**, not either
  estimate. *(The discrepancy itself was found 2026-09-21; whichever figure is
  right, the other must be corrected rather than left as a second claim.)*

- ✅ **Both uncertainties above are ACCEPTED, not outstanding** *(owner, 2026-09-21:
  "i think its fine just assumes that as a drawback i am willing to accept for
  now")*. The break-even is an estimate divided by an unmeasured duration, and the
  run volume is two figures in two documents. **Neither blocks anything**: the store
  has three orders of magnitude of headroom either way, and the runner's ceiling is
  enforced by FR-22 at runtime rather than predicted here. **Recorded as a decision
  so a later reader does not rediscover them as defects** — the probe resolves both,
  and this bullet is what should be deleted when it does.
- **FR-22 now governs these minutes**, amended the same day. Before that the
  dispatcher enforced only the model provider's windows, so a private-repo run spent
  a budget no requirement knew about.
- **Deliberately NOT chosen: a purpose-built agent sandbox** (E2B, Daytona, Modal).
  They are better at sub-second cold starts, filesystem snapshots and thousands of
  concurrent sandboxes — none of which arrive at one operator and ~100 runs/month,
  where a ~20-second provisioning cost is 1% of a 30-minute job. They would also put
  the runner off-GCP *and* owe NFR-2 a retention review. *(⚠️ That second
  clause is VOID from 2026-09-21 — NFR-2 now selects on performance alone, so
  no provider owes a retention review. The off-GCP argument stands on its own
  and this decision is unaffected.)*
