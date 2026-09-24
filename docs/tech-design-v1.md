---
title: Forge Wingman — tech design v1
date: 2026-09-21
tags: [tech-design, forge-wingman, go, firestore, github-actions, cloud-run, opencode]
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
| **webhook** | `adr/0013` — enqueue in seconds | Linear `AgentSessionEvent`, public, signature-verified | unattended |
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
the next run  =  state == "queued"  ORDER BY linear_priority  LIMIT 1
```

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
go 1.22              // language floor - ServeMux method and wildcard patterns
toolchain go1.27.1   // what builds it; raise deliberately
```

**The floor is 1.22 and the reason is load-bearing:** `net/http`'s `ServeMux`
gained method and wildcard routing there, which is the whole argument for using
the stdlib instead of a third-party router. On 1.21 that routing does not exist.
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
5. **Select** the next run per FR-22, in Linear priority order, dispatching only if
   the estimated cost breaches no ceiling. A deferral records the *binding* ceiling.
6. **Claim** the selected run with a Firestore transaction — claim only if still
   `queued`, so no two polls can dispatch the same run. "No document returned"
   means another poll won.
7. **Dispatch** by `workflow_dispatch` against the target repository.
8. **Notify** per FR-23 — link-only, and only for the five recurring classes
   (FR-11, FR-12, FR-18, NFR-1).

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
| `opencode run` for the plan phase, `edit`/`bash` denied | FR-3 |
| `opencode run` for the build phase | FR-4 — every edited file must appear in the plan's list |
| open the PR, state decided at creation | FR-5 — never transitioned afterwards |
| write the run record and upload completions | FR-6, and NFR-7's stdout prohibition |

**Gates are enforced in three places, deliberately.** opencode's per-agent
`permission` block denies the tool; the runner refuses and records; and the
`GITHUB_TOKEN` is scoped so the capability is **withheld** rather than merely
denied — the runner never holds permission to mark a PR ready or to merge, so
FR-5's "the runner cannot change it" is structural rather than conventional.

**The 14 GB runner disk is the real ceiling.** Shallow clones (`--depth`) and
`actions/cache` for the dependency tree. Exhaustion is an **infrastructure fault
per FR-13** — the run terminates and never escalates a model tier, because no
model can fix a full disk.

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

### AI — TWO seams, and they are not the same shape

⚠️ **This section previously said opencode "is the whole AI layer". It is not.**
There are two model seams and they differ in shape, cost model, latency and
failure mode — which is why they get separate providers rather than one
abstraction spanning both. Unifying them because they are both *AI* would be
picking the wrong axis. See `adr/0010`.

#### The BUILD seam — opencode

**opencode is the harness.** Per-agent `model`, `prompt` and `permission`;
provider OpenCode Go, with pay-per-token documented as the fallback. No
orchestration framework and no Python — `adr/0005`. This seam generates text,
runs long, costs real tokens, and is what FR-22's allowance windows ration.

#### The DECISION seam — FR-15's sizer

**It is not an opencode call, and it never touches the build path.** The sizer
runs inside the DISPATCHER, before a run is enqueued at all — step 3 of the poll
above. opencode is the runner's harness; the two never meet.

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
is that OpenCode Go is $10/month flat against per-token, which FR-6 measures.

⚠️ **Pin the version either way** — `~typesafe/jev-latest` floats, and a
floating id lets the model change with no configuration edit, which contradicts
FR-14's premise that the model IS configuration.

**The rung, per `engineering-defaults`' own test.** It quotes Anthropic:
**workflows** are *"LLMs and tools orchestrated through predefined code paths"*,
**agents** *"dynamically direct their own processes and tool usage"*. Forge
Wingman's agent is squarely the second — and that agent is **opencode's**, not
ours. The orchestration already exists as a third-party harness we invoke, so a
graph engine would be orchestrating an orchestrator.

> ⚠️ **This reaches the same conclusion as Forge Octant's `adr/0003` from the
> opposite premise, and the difference matters.** That product's argument is that
> *the model never picks the path*. Here the model does pick the path — we simply
> do not own the layer where it happens.

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
- **It stretches the ALLOWANCE, not the cash.** At level 5 the rule stack is about
  $4.82/month uncached and $0.96 cached — a rounding error against NFR-1's $20.
  What matters is that NFR-1 names the allowance windows as the binding
  constraint, so a 5x cut on the largest token line is roughly 5x more runs before
  FR-22 starts deferring. Caching is what decides how soon the ladder stalls.

**Evaluation is the run record, not a tracing vendor.** FR-6 already persists model
per phase, tokens in/out/cached, cost, duration per phase and outcome — which is
also what FR-21's ADMISSION gate reads, since a fabricated value or a runaway
tool-call count is only visible in the record. *(Amended 2026-09-22: this cited
FR-21 promoting a challenger on rewrite rate over ≥10 sampled tickets.
Champion-challenger was superseded, and the dependency on FR-6 is STRONGER rather
than weaker — admission needs per-run evidence.)* Langfuse would be a
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
| 2 | **Superseded (2026-09-23):** probe cells ran **0.9–8.1 min, median 5.5**, so the ~30 min figure is falsified — but a full plan-build-PR run is still unmeasured, and it decides whether level 2 is free or metered. ~~Run duration is assumed at ~30 min and has never been measured~~ | nothing; FR-6 records it and NFR-4 says measure before tuning |
| 3 | FR-20's PR actions need a scoped GitHub credential for the operator; the scope set is enumerated but not created | the retry-and-act phase only |
| 4 | **The decision seam's provider is admissible but unproven.** Jev fits FR-15's shape exactly and NFR-2 no longer bars it, but it has not been scored against the held-out set and it is six days old | nothing at level 0 — FR-15's deterministic signals run first and free, and the classifier decides only the remainder |
