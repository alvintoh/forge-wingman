---
title: Forge Wingman — tech design v1
date: 2026-09-21
tags: [tech-design, forge-wingman, go, firestore, github-actions, cloud-run, command-code, omp]
status: draft
version: 1
---

# Forge Wingman — tech design v1

> **Input:** `prd-v1.md` (FR-1..25, NFR-1..8). This document settles *how*; the PRD
> owns *what*, and where they disagree the PRD wins and this file is wrong.
>
> **Supersedes nothing.** `business-requirements-v1.svg` and `operating-model-v1.svg`
> are deliberately technology-free; `architecture-v1.svg` is the only drawing here
> that names a runtime.

## General

**The shape, named: four deployables sharing one store** (the fourth, the webhook, since `adr/0013`). Not microservices —
there is no service mesh, no inter-service call, and nothing to orchestrate. Not a
monolith either. Each deployable has a different *trigger* and a different
*attendance model*, which is the only reason they are separate.

| Container | Forced by | Trigger | Attendance |
|---|---|---|---|
| **dispatcher** | FR-1 poll, FR-15 sizing, FR-22 selection, FR-23 notice | the webhook, or Cloud Scheduler ≤15 min as backstop | unattended |
| **webhook** | `adr/0013` — enqueue in ~2 min (job start-up) | Linear `AgentSessionEvent`, public, signature-verified | unattended |
| **runner** | FR-2 worktree-per-run, FR-3/4 phases | dispatched per run | unattended |
| **surface** | FR-20 browse + statistics, FR-24 retry, FR-25 keyboard | HTTP, behind IAP | interactive |
| **store** | FR-6 record + completions, NFR-7 90-day bound | — | foundational |

The dispatcher and runner **cannot share an execution unit**: a ~30-minute run
in-process blocks the next poll and breaks FR-1's 15-minute promise.

### ~~Two containers~~ One container that deliberately does NOT exist

**Superseded (2026-09-24) by `adr/0013`:** the webhook receiver now exists, and the poll stays as its backstop. ~~No inbound webhook receiver.~~ FR-1's own note settles it — *"the 15-minute
tolerance is deliberate: it permits a polled implementation and therefore needs no
always-on inbound endpoint."* An endpoint would be a fourth deployable, a public
attack surface and a signature to verify, all to buy latency the requirement
explicitly declines.

**No message queue.** `engineering-defaults` records *Pub/Sub → Cloud Run jobs* for
async work, and it is the wrong primitive here — see `adr/0003`. FR-22 says the
dispatcher *"considers eligible tickets in **Linear priority order**, dispatches only
if the run's estimated cost would breach no ceiling, and otherwise records a
deferral … and reconsiders at the next poll."* That is a **query re-evaluated every
poll**, not a delivery. Pub/Sub has no priority ordering and Cloud Tasks cannot
re-rank, so either would be fought rather than used. The store is the queue:

```
admission  =  state == "queued"  ORDER BY linear_priority, queued_at
              → admit while FR-22 and FR-27 hold, up to N in flight
```

*Updated (2026-09-25):* this read `the next run = … LIMIT 1`. FR-27 made parallel
runs the default, so the query is walked rather than truncated; `adr/0003` records
the claim transaction and the `dispatch/ledger` document that makes reservations
exact.

*Updated (2026-10-13), forge-wingman #116:* the order gains a tie-break, `queued_at`
(oldest first; stamped at enqueue and requeue, kept when a run is answered or
released), and the webhook refreshes a queued or planned run's stored priority
(`adr/0013`). `adr/0003` records the third index and the `backfill-queued-at` step.

**Where the DLQ went.** `engineering-defaults` requires a DLQ and bounded retry on
every async consumer. With no queue there is no dead-letter topic, and the
requirements already carry both halves: FR-13 bounds retry at one escalation, and a
run that exhausts it terminates with a record naming both attempts. The record *is*
the dead-letter store, and it is queryable, which a DLQ is not.

### Language

**Go for all three, decided per component and not by default.** The full
comparison against Rust is in `adr/0009`, and three things about it matter more
than the verdict.

**It is not a performance decision.** Every component is I/O-bound glue against
Google APIs — one poll is ~280ms wall clock against ~2ms CPU busy, and Cloud Run
bills wall clock rather than cycles.

⚠️ **And it is not an ecosystem decision either — Rust is admissible here.**
`firestore-rs` is a well-maintained community crate at ~289k recent downloads
shipping ADC, transactions and aggregation; `google-cloud-storage` is first-party
Rust. An earlier draft rejected Rust on ecosystem and was wrong; `adr/0009`
records the measurement so the objection is not re-raised.

**What decides it is fit and cost:** a poller, a REST surface and a subprocess
runner sit in Go's column on every mainstream comparison; the canonical Go→Rust
migration case needs a hot path, a cache and a measured GC pause, none of which
exist here; the compile loop lands on the tuning phase; and Rust would be a fourth
ecosystem maintained solo. `engineering-defaults` reaches the same place by the
seam test: *"a standalone service, webhook receiver, media/PDF processor,
scheduled job or proxy has no seam to lose, so Go is its plain merit."*

⚠️ **Toolchain tracks LATEST STABLE; dependencies are PINNED.** Two different
rules because the guarantees differ: Go's compatibility promise makes a compiler
upgrade low-risk and it carries security fixes, while a transitive dependency
carries neither — and this runner is UNATTENDED, where §3a sends anything that
fails silently at 06:02 to boring-and-stable. **"Latest" is also not writable**, so
what goes in `go.mod` is a FLOOR plus the toolchain that builds it:

```
go 1.27              // language floor - the toolchain's minor, the only one CI builds
toolchain go1.27.1   // what builds it; raise deliberately
```

**The floor matches the toolchain's minor, because a floor is only true if
something builds at it.** CI reads `go-version-file: go.mod` and so compiles with
the toolchain alone; a lower floor would promise versions nothing ever tests.
This is an application shipped in its own image, not a library, so no importer
needs an older floor. *Updated (2026-09-24):* the floor was `1.22`, justified by
`net/http`'s `ServeMux` gaining method and wildcard routing there, which is the
whole argument for using the stdlib instead of a third-party router. That reason
still sets a hard minimum (on 1.21 the routing does not exist) but it never argued
against a higher floor, and `1.22` was never built by anything. Raise the floor
with the toolchain, in the same change.
Pinned does not mean stale — Dependabot or Renovate raises dependencies
deliberately, with CI proving each one. *(Recorded 2026-09-22: the owner's machine
was on go1.21.3, six releases behind and below the floor, and nothing in the design
stated a version where a build could enforce it.)*

⚠️ **`exhaustive` and `errcheck` are REQUIRED CI gates**, standing in for a type
system on FR-7 through FR-12's gate classes. That is the honest cost, and the
mitigation the decision depends on.

**TypeScript for the surface's client only**, as its own project (`web/`) — a
per-*project* split rather than a per-*layer* one — ordinary practice, and the
boundary is an HTTP contract of three endpoints rather than a shared type system.

## Frontend

**A Vite SPA — React + TanStack Router + TanStack Form — built to static assets and
served by the Go surface binary via `embed.FS`.** Not Next.js; `adr/0006` records
why.

**React is required, and by a requirement rather than a preference.** FR-24 retains
an unsubmitted note per row, restores it when the sheet reopens, keeps it
session-scoped and deliberately loses it on reload; FR-25 wants a roving-tabindex
listbox where Escape returns focus to the originating row *and keeps that note*.
That is the browser holding state between saves, which is `/tech-design` §3d's test
for a client runtime.

**It also fails the GoTH route on its own terms.** The Go stack pack records that
htmx does not handle keyboard shortcuts — so FR-25's entire focus model would be
hand-written JS, and htmx would be replacing a `fetch` call with an attribute while
the hard part stayed manual.

- **TanStack Form**, not React 19 form actions: `engineering-defaults` states that
  *"a client-only SPA loses PE entirely and should default to TanStack Form
  instead"*, and progressive enhancement is worth nothing on an IAP-fronted
  single-operator tool.
- **TanStack Router** for the three routes — inbox, run detail, statistics.
- **Tailwind v4 via `@tailwindcss/vite`**, installed with Bun beside the SPA — the
  standalone CLI is Tailwind's pick only where no `package.json` exists, and `web/`
  has one. Node is a build-time dependency only. *(Corrected 2026-09-23; see `adr/0006`.)*
- **Tokens** are generated, not transcribed. `design/tokens.py` is the single
  source; `theme.css` is the Tailwind `@theme` projection the build imports
  (generated in the vault's `design/`, copied to the repo's `web/src/`); `design-tokens-v1.md` explains the mapping and deliberately restates no
  values. `adr/0001` is the reasoning behind all three.

**Accessibility is a requirement here, not a practice.** NFR-8 sets WCAG 2.2 AA and
FR-25 names the WAI-ARIA APG listbox pattern explicitly: one tab stop, arrow keys
moving focus within the list, Enter opening the sheet and moving focus into it,
Escape returning focus to the row that opened it.

## Backend

### Dispatcher — Cloud Run job, woken by the webhook or Cloud Scheduler

One Go binary entrypoint (`cmd/dispatcher`). Per poll:

1. **Identity first.** FR-12 verifies the active git and `gh` identity match the
   configured personal account and refuses to start otherwise.
2. **Read the webhook's new-ticket rows, then poll Linear as the backstop** (`adr/0013`) for issues in the Forge team **delegated to the Forge Wingman
   app** — an app cannot be an assignee in Linear, so delegation is the trigger and
   the human assignee is left intact. Requires an OAuth app installed with
   `actor=app` and the `app:assignable` scope; see FR-1.
3. **Size** each new ticket — FR-15. Deterministic signals first and free:
   `/ticket-cut` title prefixes, keyword matches on schema/API/auth/money, whether
   the ACs touch more than one module, whether tests cover the named area. The
   classifier decides only the remainder, and a manual Linear label always overrides.
4. **Enqueue or reject** — a ticket above L is rejected with the reason recorded.
5. **Admit** runs in Linear priority order, oldest-queued first among equals, while every FR-22 and FR-27 condition
   holds — budget reservations, size, interference, the owner's runs-per-repo cap, the review WIP limit, platform
   caps, the circuit breaker — up to N in flight. A run that waits records the
   *binding* condition.
6. **Requeue a run stopped on a transient fault** (FRG-132). A run's stop carries a
   class when its recorded detail — or, for a stop that means a job failed outside the
   code (setup, the worktree, the PR job, no readable summary), the failed job's own log
   tail, which the runner reads at record time — names one of a closed list of transient
   signatures — a Go module proxy or checksum read, a network timeout, a lost runner.
   An abandoned claim carries none: it is as likely cancelled by the owner as lost. The poll
   returns such a run to the queue for up to two more attempts, each
   recorded with its class, ahead of the claim walk so the retry can be claimed by the
   same poll. The requeue only returns the run to the queue: the claim that follows is
   admitted like any other, so the ceilings, the review WIP limit and the breaker hold
   it. A stop with no matching class is never requeued on its own, and a run that has
   used its attempts stays stopped with a notice. A requeue or an abandon puts the
   ticket back to the state it held before the claim — the dispatcher's own
   requeue in the poll, and an operator's `runner requeue` — so a ticket is In
   Progress only while a run holds it.
7. **Claim** each admitted run with a Firestore transaction over the run row and
   `dispatch/ledger`: claim only if still `queued` and the reservation still fits,
   so no two polls dispatch the same run or overbook a window. "No document
   returned" means another poll won. The claim then moves the run's ticket In
   Progress, under the state id its own team's workflow names — a ticket already
   started (In Review among them), finished or canceled is left where it is —
   writing back the manual order a state change rewrites and reading both back,
   and records the state the ticket held before the claim on the run row. A
   Linear write that fails is logged, with the reason, and never withholds the
   run's start.
8. **Dispatch** by `workflow_dispatch` against the target repository.
9. **Notify** per FR-23 — link-only, and only for the five recurring classes
   (FR-11, FR-12, FR-18, NFR-1, FR-26's infrastructure stop). The same classes trip
   FR-27's circuit breaker, so no further run is admitted until a manual reset —
   except FR-26's infrastructure stop, which counts toward the provider halt instead.
10. **Ask the owner, at the poll's end** (FR-33). A run whose plan raised a decision it
   may not take alone parks in the `waiting` state — holding no runner and no
   reservation — while the poll posts that decision into the run's Linear agent
   session as one selection activity: at most four options, the recommended first,
   each carrying its cost. A reply matching an option resumes the run with the answer
   on its record (its `decisions` list, kept outside `runner.Record` so `Finalize`
   cannot clobber it); a free-text reply matching none re-posts the same options and
   never guesses; and no reply within the configured window (default 24 hours) stops
   the run as waiting-on-owner with one notice. The ask is once-only because the row
   records when it was posted, so the question is neither dropped nor repeated, and
   the dispatcher is its one decider (`adr/0013`) — the poll both posts and matches.

11. ~~**Delegate the queue's next ticket itself** (FRG-125).~~
    > **Retired (2026-10-19):** the dispatcher never delegates a ticket; work starts only when the owner delegates one (`adr/0018`). The paragraph below describes what was built (FRG-125, FRG-164) and removed (FRG-181); the eligibility rule it used is in git at `bc77711` for the surface's START PHASE to restore from.
    The poll delegates the next eligible ticket to the app while today's claimed runs are
    below the daily bar it was configured with and the
    review WIP limit has room. Eligible means an open ticket in the earliest milestone that
    still has open tickets, ordered by Linear priority: a `[BE]`, `[FE]` or `[SLICE]` title,
    a `repo:` label on the runner's allowlist and a `size:` label, no `.github/workflows/`
    path in its body, no `wingman:hold` label, and no run row that has ended (step 6
    requeues a transient stop itself; every other ended run needs an operator's
    `runner requeue`). A ticket with an open blocker is delegated, since the dispatcher
    holds it anyway. It runs last in the poll, and the agent session a delegation opens
    wakes the poll that admits it.

**Why a Job and not a service:** nothing calls it over HTTP. It is started by the schedule or by the webhook's run request (`adr/0013`), and the public endpoint lives in the webhook service, not here.

### Webhook — Cloud Run service, public, signature-verified

One Go binary entrypoint (`cmd/webhook`), built in Phase 3. It verifies Linear's
`Linear-Signature`, writes a new-ticket row keyed on the issue id, starts a dispatcher
execution and returns inside Linear's 5-second limit. It decides nothing — `adr/0013`.

### Runner — a Go binary invoked by a GitHub Actions reusable workflow

**Per target repository, never centrally.** `adr/0002` records the three
independent reasons — log exposure, billing, and GitHub's own acceptable-use
language. A reusable workflow living in each target repo, called with the run id.

| Step | Requirement |
|---|---|
| fresh VM, fresh checkout | FR-2 isolation, free — nothing to build |
| create the worktree and branch | FR-2 |
| fetch the projection for the current rule-stack sha from Cloud Storage; refuse if absent | FR-19 — full for plan, trimmed for build; `adr/0012` |
| the harness's read-only profile for the plan phase, edit and shell refused (omp: `--approval-mode always-ask`) | FR-3 |
| the harness's build profile for the build phase (omp: `--approval-mode yolo`) | FR-4 — an edit outside the plan's list drafts the PR and is listed in it |
| the repo's checks, fed back to the builder for up to 3 rounds; then one review by a different model against the ACs | FR-28 — same tier throughout, so never FR-13 escalation |
| open the PR, state decided at creation | FR-5 — never transitioned afterwards; still-failing checks or open findings make it a draft |
| write the run record and upload completions | FR-6, and NFR-7's stdout prohibition |

~~*Updated (2026-10-05):* the two phase rows named `opencode run`; the harness is
Command Code since the record then numbered `adr/0015`.~~

*Updated (2026-10-07):* the two phase rows named Command Code's `--plan` and `--yolo`.
The harness is now configuration per plan: omp by default, opencode as fallback
(`adr/0015`, FRG-57, FRG-63, FRG-65).

**Gates are enforced in three places, deliberately.** The harness's restricted
profile denies the tool (*Updated (2026-10-07):* each adapter's read-only profile,
which the conformance suite checks refuses edit, write and shell — omp's
`--approval-mode always-ask` by default, `adr/0015`; this said Command Code's
`--plan`, and before that opencode's per-agent `permission` block); the runner refuses and records; and the
capability is **withheld** rather than merely denied — the job the model runs in
never holds permission to mark a PR ready or to merge, so FR-5's "the runner
cannot change it" is structural rather than conventional.

*Updated (2026-09-25):* this said the `GITHUB_TOKEN` is *scoped* to withhold it.
Scope alone cannot: creating a PR and marking it ready are the same
`pull-requests: write`, the only PR scope GitHub offers. The withholding is a
**job boundary** — the model's job holds a read-only token, and a separate job
running fixed steps opens the PR. Each job is a fresh VM, so the split is real.

**Every target repository must allow Actions to create pull requests.** GitHub's
*Allow GitHub Actions to create and approve pull requests* setting decides whether
`GITHUB_TOKEN` *"can create and approve pull requests"*, and it starts off, so a
repository onboarded without it runs the whole build and then fails at the PR.
Since the runner is per repository (`adr/0002`), this is a per-target onboarding
step, not a one-off. It also permits approving, which grants nothing while no
branch rule counts approvals. *(Found 2026-09-25 while grounding FRG-13:
forge-wingman read `can_approve_pull_request_reviews: false`.)*

**Actions artifacts inherit the repository's read access**, so on a public target
they are public. Anything private to a run — the projection, the completions —
moves between the runner and Cloud Storage directly and never through an
artifact; only the diff, which becomes a public PR anyway, may cross jobs as one.

**The 14 GB runner disk is the real ceiling.** Shallow clones (`--depth`) and
`actions/cache` for the dependency tree. Exhaustion is an **infrastructure fault
per FR-13** — the run terminates and never escalates a model tier, because no
model can fix a full disk.

### PR review — a workflow from main, and what it does when the reviewer is out

`pr-review.yml` runs as `pull_request_target` from main's copy and never runs the PR's
code. Three jobs each hold one credential: `context` a read token, `review` the model
key alone, `post` the comment and status writes plus the App token for the merge
request. It posts one marked comment and the `wingman-review` status, and requests
auto-merge only for a clean review of an eligible PR, pinned to the reviewed head
(`FR-16`). Branch protection requires the `go` and `web` checks and no approval.

**Auto-merge — *Updated (2026-10-19)*, `adr/0018`.** Whether an eligible PR may merge at
all is one metric-gated rule and the owner's switch: the switch is stored at
`dispatch/auto-merge`, set with `runner auto-merge` and by the surface's own handler, and
auto-merge is on when the trailing rewrite rate is 10% or less over the ten newest measured
merged PRs and none of them is an auto-merged PR a later merged revert names. pr-meta marks
a PR eligible only when the rule is on and the switch is on. It replaces the
`WINGMAN_AUTO_MERGE` repository variable, which the dispatcher could not read.

**Front-end hold — *Updated (2026-10-13)*, forge-wingman #115.** Eligibility for auto-merge
(`FR-5`) excludes an S or M PR that touches the front end, which stays ready, not a draft.
It is held when the ticket carries the `area:fe` Linear label (carried on the ticket and
the run record, set only by the dispatcher) or a changed file falls under a prefix
declared for its repository in `WINGMAN_FRONT_END_PATHS`, a JSON object of repository to
prefixes. The two failure directions differ on purpose: unset holds nothing on paths,
while an unparseable value holds every PR. A rename reports both its old and new path.
The PR body names why it waits and tells the reviewer to run `/fe-sweep` before
`/pr-review`.

**When the reviewer is unavailable — decided 2026-10-12 by the owner; not yet built.**
A review whose models all fail returns **no verdict**, which today requests nothing
and leaves the PR to the owner (seen on two PRs on 2026-10-11 and 2026-10-12, when the
GOAT allowance was spent). The decision splits the two causes a no-verdict hides:

| Review ended | Today | Decided |
|---|---|---|
| clean | auto-merge requested | unchanged |
| findings | waits for the owner | unchanged |
| no verdict — **allowance exhausted** | waits for the owner | requests auto-merge if the PR is eligible and `go` and `web` pass at the reviewed head |
| no verdict — **failed or timed out** | waits for the owner | unchanged, still waits |

An allowance runs out on a schedule and refills, so it is not a fault of the
reviewer, and holding every eligible PR behind it stalls the unattended loop for
hours. **What it accepts:** an S/M PR with zero open boxes merges having had no
review beyond its own checks and the build's pre-PR review (`FR-28`), so a defect
those cannot see ships. The comment states it plainly (*merged without review:
reviewer allowance exhausted*), and the eligibility rules and the `FR-17` sample bound
how often it applies. *(Judgment call, the owner's; no authority prescribes it.
Revisit when review models fall through the ordered plan list, `FR-14`, which would
make the case rarer.)*

**An allowance here means any reviewer's, and each has its own marker.** The product's
own review runs its models on a coding plan, whose exhaustion the plan's allowance
markers name (`FR-26`). GitHub Copilot's review is auto-requested on every PR and draws
a per-user quota; when that is spent its review reads *unable to review this pull
request because the user who requested the review has reached their quota limit*
(seen on three PRs, 2026-10-11 and 2026-10-12). Copilot gates nothing (the branch rule
requires no approvals), so its quota being out holds no PR and needs no handling beyond
being recognised as that state, not as a finding. The builder's own plan allowance is a
different matter: admission already defers or falls through it (`FR-22`, `FR-26`) and
it never reaches the merge.

**It needs a classifier the review does not have.** "No verdict" is not evidence of an
exhausted allowance: the two failures above read *agent failed* and *timed out*, which
fits a spent allowance without proving one. The cause must come from the reviewer's own
marker, carried through the verdict step as a closed state, never inferred from the
absence of a verdict. Until that exists the rule is inert and every no-verdict PR waits.

### Surface — Cloud Run service behind IAP

One Go binary entrypoint (`cmd/surface`) serving the embedded SPA plus three JSON
endpoints: list runs with filters, read one run, and act on one run. Reads
Firestore directly; writes to GitHub through the **owner's** credential, because
FR-20 requires each write action be performed *as me, not as the runner*.

**Completions are never proxied through the service.** `engineering-defaults`:
*"never proxy bytes through the app — issue a signed URL and let the client talk to
the bucket directly."* The surface issues a short-lived signed URL for a
completion; the browser fetches it from Cloud Storage.

**Which gates apply to the surface is settled by FR-20 and not re-derived here:**
FR-8 and FR-11 bind whoever acts, so they apply. FR-7, FR-9 and FR-10 gate the
*agent* from reaching a person, guessing, and choosing — and the operator is the
person, doing the choosing they opened the surface to do.

**The Autonomy statistics are computed at read time from one capped query of measured rows, with repo, size and models written onto the row when it is measured; no rollup until the revisit triggers in `adr/0016` fire.**

### AI — TWO seams, and they are not the same shape

⚠️ **This section previously said opencode "is the whole AI layer". It is not.**
There are two model seams and they differ in shape, cost model, latency and
failure mode — which is why they get separate providers rather than one
abstraction spanning both. Unifying them because they are both *AI* would be
picking the wrong axis. See `adr/0010`.

#### ~~The BUILD seam — opencode~~ ~~The BUILD seam — Command Code~~ The BUILD seam — the harness

**Superseded (2026-10-07) by `adr/0015`, in its harness:** the plan and the harness
are separate configuration. The model prefix names the plan; the harness is a default
and an ordered fallback per plan, behind the `Harness` interface, and every adapter
passes one conformance suite (FRG-62, FRG-57). Today omp is the default (FRG-63) and
opencode the fallback (FRG-65); the Command Code CLI is retired, its adapter
removed after FRG-65. The plan is still GOAT, reached through Command
Code's Provider API.
*Updated (2026-10-12):* plans are now an ordered list in configuration — OpenCode Go
first once its session header and terms clear, GOAT second; a ticket keeps one plan for
its whole life and falls through at admission (PRD FR-14, FRG-54, `adr/0015`).

~~**Superseded (2026-10-05) by the record then numbered `adr/0015`:** the harness is the Command Code CLI and the
provider its GOAT plan ($10/month; $14/5h, $35/7d, $70/month). The seam's shape below
is unchanged.~~ *The GOAT plan and its windows stand.*

**opencode is the harness.** Per-agent `model`, `prompt` and `permission`;
provider OpenCode Go, with pay-per-token documented as the fallback. No
orchestration framework and no Python — `adr/0005`. This seam generates text,
runs long, costs real tokens, and is what FR-22's allowance windows ration.

#### The DECISION seam — FR-15's sizer

**It is not a harness call, and it never touches the build path.** The sizer
runs inside the DISPATCHER, before a run is enqueued at all — step 3 of the poll
above. The harness is the runner's; the two never meet. *Updated (2026-10-07):
this named opencode, `adr/0015`.*

**Its shape is a classification: unbounded in, bounded out.**
`engineering-defaults` names exactly this as the cheapest thing to buy, *"because
a closed output set is gradeable against decisions humans already made"* — and
FR-15 already specifies the grading set, a held-out set of hand-sized closed FRG
tickets.

**FR-15 needs a CALIBRATED probability, which is the whole reason this seam is
separate.** It demands two thresholds off one classifier because the decisions
differ in reversibility — S/M loose, M/L strict with *"any doubt routes to
review"*. A threshold is meaningless without calibration, and asking an
autoregressive model to emit a confidence number does not produce one: it says
0.9 and that figure means nothing in particular.

**The decision seam also runs off the allowance windows — but that is a minor
benefit, not the reason.** Quantified 2026-09-21: sizing ~200 tickets a month at
~2k tokens each consumes **0.2–2% of the $60 monthly allowance** and 0.1–1% of a
$12/5h window in a ticket-cut burst. Worth having, nowhere near worth adopting a
provider for. **Calibration is the reason; budget isolation is a side effect**
— recorded this way because an earlier draft had them the other way round.

✅ **Jev (TypeSafe AI) is ADMISSIBLE as of 2026-09-21.** It was excluded hours
earlier on NFR-2's zero-retention half; NFR-2 now selects on performance alone,
so that bar is gone. **Admissible is not adopted** — FR-15's held-out set is
still the acceptance test, and the remaining objection is maturity rather than
policy: A
System One model: non-autoregressive, returns typed decisions with calibrated
probabilities, 70-500ms, $0.042/M input and output free (verified 2026-09-21).
One caution stands: it launched 2026-09-15, and this is an **unattended**
component where §3a's bias is boring and stable. **FR-26 blunts that
considerably** — if it disappears, sizing falls back to FR-15's free
deterministic signals and the below-threshold route is plan-it-and-review, so
its failure costs the classifier's marginal accuracy rather than the product.

**Direct or via a gateway — open, and now on COST alone.** Jev is on OpenRouter
as `typesafe/jev-1.13` at the same price, which is useful for comparing several
classifiers and would collapse two credentials into one. The argument against a
gateway was that NFR-2 bound every party in the data path; **NFR-2 selects on
performance as of 2026-09-21**, so a gateway costs no verification. What remains
is that GOAT is $10/month flat against per-token, which FR-6 measures once each harness's
runs carry a cost (*Updated (2026-10-07):* omp's runs are priced from tokens × the plan's rates
(FRG-57), as the opencode fallback's must be (FRG-65); the retired Command Code CLI adapter is removed after FRG-65. *Updated (2026-10-05):* this
named OpenCode Go).

⚠️ **Pin the version either way** — `~typesafe/jev-latest` floats, and a
floating id lets the model change with no configuration edit, which contradicts
FR-14's premise that the model IS configuration.

**The rung, per `engineering-defaults`' own test.** It quotes Anthropic:
**workflows** are *"LLMs and tools orchestrated through predefined code paths"*,
**agents** *"dynamically direct their own processes and tool usage"*. Forge
Wingman's agent is squarely the second — and that agent is **the harness's**, not
ours (*Updated (2026-10-07):* this said opencode's; `adr/0015`). The orchestration already exists as a third-party harness we invoke, so a
graph engine would be orchestrating an orchestrator.

> ⚠️ **This reaches the same conclusion as Forge Octant's `adr/0003` from the
> opposite premise, and the difference matters.** That product's argument is that
> *the model never picks the path*. Here the model does pick the path — we simply
> do not own the layer where it happens.

**Superseded (2026-10-07) by `adr/0015`, in its mechanism:** caching is per harness
adapter, and the runner measures that the stable rules prefix (FRG-50) holds under
each. omp's static prompt precedes its per-directory block, so it caches across
directories on its own.

~~**Superseded (2026-10-05) by the record then numbered `adr/0015`, in its mechanism:** the `x-opencode-session`
header below was opencode's. Under Command Code a per-run HOME and
`--resume <sessionId>` keep a phase's prefix warm within a run; whether a retry
reuses the original run's cache is unmeasured, so the ticket-keyed bullet is
unverified.~~ The throughput argument stands.

**Prompt caching is a throughput mechanism, and it constrains the projection.**
The provider exposes a *Cached Read* price on every model and asks for one thing:
*"Send a stable session ID in `x-opencode-session` for each conversation so we can
optimize routing and prompt caching"* (verified 2026-09-21). Three consequences:

- **The session id is keyed on the TICKET, not the run.** FR-24's retry is a new
  run on the same ticket with a note appended, so a ticket-keyed id lets the
  retry hit the prefix the original warmed. The id is a caching hint and carries
  no identity, so this does not blur two run records — they stay linked by run id.
- **Keyed per PHASE as well**, because FR-19 emits a full projection for plan and
  a trimmed one for build. Two different prefixes are two different caches.
- **It stretches the ALLOWANCE, not the cash.** The rule stack is about
  $4.82/month uncached and $0.96 cached — a rounding error against NFR-1's $20.
  What matters is that NFR-1 names the allowance windows as the binding
  constraint, so a 5x cut on the largest token line is roughly 5x more runs before
  FR-22 starts deferring. Caching is what decides how soon the budget stalls.

**Evaluation is the run record, not a tracing vendor.** FR-6 already persists model
per phase, tokens in/out/cached, cost, duration per phase and outcome — which is
also what FR-21's ADMISSION gate reads, since a fabricated value or a runaway
tool-call count is only visible in the record. *(Amended 2026-09-22: this cited
FR-21 promoting a challenger on rewrite rate over ≥10 sampled tickets.
Champion-challenger was superseded, and the dependency on FR-6 is STRONGER rather
than weaker — admission needs per-run evidence.)* *(Amended 2026-10-14: the
record also carries the files each phase's agent read and the largest context any
one request read, and a build phase that completes a run of requests with no file
change is cancelled while it runs and ends as `agent-no-progress`, unless a real
agent failure or the timeout ended it first — a loop over reading is otherwise
indistinguishable from slow work over a large context in the token totals.)* Langfuse would be a
second copy of data the product must hold anyway. It stays available — it is
OTEL-native, so Go exports to it — if tracing is ever wanted beyond the record.

## Infra

| Concern | Choice | Cost |
|---|---|---|
| run records | **Firestore** — 1 GiB, 50k reads / 20k writes **per day** | $0, three orders of headroom |
| completions | **Cloud Storage**, `age: 90` + `Delete` lifecycle rule | $0 within 5 GB-months |
| projections | **Cloud Storage**, `projections/<sha>/`, published on every push to the rule stack, never expired | $0 — ~0.3 MB a push; `adr/0012` |
| dispatcher | Cloud Run **job** | $0 — seconds, 96×/day |
| surface | Cloud Run **service**, scale to zero | $0 at a few requests/day |
| runner | **GitHub Actions**, per target repo | $0 public; metered private |
| schedule | **Cloud Scheduler** — 3 jobs free per *billing account* | $0 |
| auth | **IAP** | see `adr/0007` |
| IaC | **OpenTofu** from the first deploy | `adr/0008` |
| images | GHCR, per `engineering-defaults` | $0 |
| dev loop | `make dev` · `make ui` (process-compose); Vite HMR, `air` for Go | $0 — local only, per `engineering-defaults` §Local DX |

*Every figure verified 2026-09-21 against the vendor's own page, except Cloud Run's
own free tier — `cloud.google.com/run/pricing` redirects in a loop, and the PRD's
recorded 180,000 vCPU-seconds disagrees with a secondary source's 240,000. Nothing
here turns on it.*

⚠️ **NFR-7's 90 days is really 97 unless soft delete is disabled.** Cloud Storage
soft-deletes for 7 days by default and the bound *"exists for privacy, not cost"* —
so the bucket disables soft delete, or the requirement is restated. Disabled, on
the requirement's own wording.

⚠️ **The retention bound is enforced by configuration, which is the point.** A
lifecycle rule cannot be forgotten the way a scheduled delete job can, and there is
no code path to review.

**FR-1's 15-minute tolerance pays twice.** It was written to permit polling; it is
also what keeps Cloud Scheduler inside three free jobs and the dispatcher's
invocation count trivial. A 5-minute poll would triple the invocations for latency
the requirement says it does not need.

## Architecture

`architecture-v1.svg` — boxes are containers, and each box's stack is drawn as
layers so a *missing* layer is visible rather than hidden behind a framework name.
It is the only diagram in this product that names technology.

## Open

| # | Item | Blocks |
|---|---|---|
| 1 | **Superseded (2026-09-23):** the probe ran — series 8 verdict in `three-trap-probe-v1.md`; its two protocol lines are not yet in the projection. ~~FR-19's three-trap probe has not been run; the trimmed projection is unproven~~ | Phase 2's first ticket |
| 2 | **Updated (2026-09-25): first full run measured** — one size-S ticket (no plan phase): build 4m13s, whole run 7m06s, ~8 billed runner minutes (each job rounds up to the minute), so ~250 runs/month fit a private target's free 2,000. One sample: close at ≥10 runs per size, with p90 for the per-run cap and the mean for minute budgets. ~~probe cells ran **0.9–8.1 min, median 5.5**, so the ~30 min figure is falsified — but a full plan-build-PR run is still unmeasured, and it decides whether the second run tier is free or metered. Run duration is assumed at ~30 min and has never been measured~~ **Updated (2026-10-17): closed for S and M** — the sum of each run's recorded phase durations on its last attempt (S n = 17, M n = 18; L has 3, so it stays open): **S mean 18.1 min, p90 32.3 min; M mean 46.7 min, p90 57.9 min**. These are phase sums, not wall clock or billed minutes (each job rounds up), so the per-run cap should read the p90 and the minute budget the mean once billed minutes are recorded per size. | nothing; FR-6 records it and NFR-4 says measure before tuning |
| 3 | FR-20's PR actions need a scoped GitHub credential for the operator; the scope set is enumerated but not created | the retry-and-act phase only |
| 4 | **The decision seam's provider is admissible but unproven.** Jev fits FR-15's shape exactly and NFR-2 no longer bars it, but it has not been scored against the held-out set and it is six days old | nothing — FR-15's deterministic signals run first and free, and the classifier decides only the remainder |
| 5 | **Updated (2026-10-07):** the cost source is per harness adapter. omp's own figure is wrong (it charged peak rates off-peak), so its runs are priced from tokens × the plan's rates (`adr/0015`, FRG-57), as the opencode fallback's must be (FRG-65); the retired Command Code CLI adapter is removed after FRG-65. **Added (2026-10-05):** a Command Code run records a provider cost of 0, so FR-22's GOAT windows and NFR-1's per-run cost cap meter nothing until FRG-47 fills the cost from the CLI transcript's `costUsd` — the record then numbered `adr/0015` | enforcement of FR-22 and NFR-1's cost cap |
