# 0010 — The decision seam is separate from the build seam

- **Date:** 2026-09-21
- **Status:** accepted — the SEAM is accepted; its provider is **admissible but
  unproven**
- **Amended 2026-09-21:** this record excluded Jev on NFR-2, and **NFR-2 was
  rewritten the same day to select on performance alone** (owner's decision:
  *"i just want pure performance only"*). The exclusion below is preserved
  rather than deleted, because the REASONING is what makes the reversal
  legible — but it no longer holds. What survives unchanged is the finding
  underneath it: FR-15's two thresholds need a CALIBRATED probability, which is
  a property rather than a product.

## Context

`tech-design-v1` said *"opencode is the harness, and it is the whole AI layer"*.
That was wrong, and the way it was wrong is worth recording: the product has two
model calls of genuinely different shape, and describing them as one layer hides
the fact that they have opposite requirements.

| | **Build seam** | **Decision seam** |
|---|---|---|
| What it does | generates code — FR-3, FR-4 | classifies a ticket — FR-15 |
| Output | unbounded text | a closed set: S / M / L / reject |
| Runs in | the runner, under opencode | the **dispatcher**, before enqueue |
| Latency | tens of minutes | sub-second |
| Cost model | metered against FR-22's allowance windows | per-call, negligible |
| Failure mode | a bad PR I rewrite in the evening | a mis-sized ticket that skips planning |
| Needs | a strong autoregressive model | a **calibrated probability** |

**The last row is what forces the split.** FR-15 demands *two separate thresholds
off one classifier*, because the two decisions differ in reversibility — S/M
loose, M/L strict with *"any doubt routes to review"*. **A threshold is
meaningless without calibration.** Asking an autoregressive model to emit a
confidence figure does not produce one: it says 0.9 and that number is not
grounded in anything. So the requirement the sizer actually has is a property the
build seam's models do not offer.

**A second reason was proposed and then QUANTIFIED AWAY, which is why it is
recorded rather than dropped.** The claim was that running classification on the
build provider makes sizing compete with building for FR-22's scarce allowance.
Priced on 2026-09-21: ~200 sizings a month at ~2k tokens is **0.2% of the $60
monthly window on a cheap model and 2.0% on a premium one**; a ticket-cut burst
of twenty at once costs 0.1–1% of a $12/5h window. It is a real saving and it is
not a reason to choose a provider. **Cash is a non-argument in both directions**
— Jev would cost $0.017/month and OpenCode Go is a flat $10 whatever the sizer
does. Calibration is the whole case.

## Decision

**Two seams, two providers, no shared abstraction.**

- The **build seam** keeps opencode and OpenCode Go, governed by FR-14, FR-13's
  tier ladder and FR-22's windows. Unchanged.
- The **decision seam** gets its own provider, **off the allowance windows**,
  configured under FR-14 the same way, and proved against FR-15's held-out set of
  hand-sized closed FRG tickets.

**No interface spans both.** An abstraction over *"generates text"* and *"returns
a typed decision"* would be unifying two things because they are both AI, which
is the wrong axis — they differ in shape, cost model, latency and failure mode.
`/tech-design` §3f's rule applies: name the workload, not the category.

**The provider is NOT chosen here.** The seam is the decision; what fills it is a
measurement, and FR-15 already specifies the measurement.

### Current candidate: Jev (TypeSafe AI)

A *System One* model — non-autoregressive, returns typed decisions with
calibrated probabilities, 70–500ms, **$0.042/M input and output free** (verified
2026-09-21). It is the only candidate found whose stated output is the property
FR-15 needs rather than an approximation of it.

**Called directly at `api.typesafe.ai`, not through a gateway**, if adopted. It is
also on OpenRouter as `typesafe/jev-1.13` behind an OpenAI-compatible endpoint
at the same price, which is worth using to compare several classifiers against the
held-out set. ⚠️ **This paragraph's objection is VOID as of 2026-09-21**: it
rested on NFR-2 binding every party in the data path, and NFR-2 now selects on
performance alone. A gateway costs no verification. What remains against it is
cost — $10/month flat versus per-token — which FR-6 measures.

⚠️ **Two cautions, recorded so neither is rediscovered as an oversight:**

1. **It launched 2026-09-15.** The dispatcher is **unattended**, and
   `/tech-design` §3a is explicit that an unattended component biases toward
   *boring and stable*, with maintenance first in the tie-break and a warning that
   "leading" is not "newest". A six-day-old dependency in something that fails
   silently at 06:02 is exactly the case that rule describes.
2. ❌→✅ **SUPERSEDED the same day — kept for the reasoning.** NFR-2 no longer
   bars anything on data handling, so this caution is void. It read:
   NFR-2 has two halves and TypeSafe passes one: its privacy policy states
   plainly that it *"will not train or fine tune any artificial intelligence or
   machine learning models on your prompts or other Input"*. But **zero
   retention is ENTERPRISE-ONLY**, by separate agreement; the standard policy
   permits retention *"while reasonably needed for service or business
   purposes"*. NFR-2 requires both and excludes otherwise *regardless of
   price* — **so as the PRD stands, Jev is excluded.**

## Consequences

- ✅ **Sizing stops drawing on FR-22's windows** — worth ~1%, so a tidiness win
  rather than a throughput one. Do not cite it as a justification.
- ✅ **The candidate is falsifiable.** FR-15 already requires proof against a
  held-out set, so Jev is run and measured rather than adopted on a launch post.
  FR-14 makes the swap configuration.
- ✅ **`adr/0005` is unaffected.** No orchestration framework and no Python: a
  single typed HTTP call from the dispatcher is not a graph engine.
- ⚠️ **A second provider is a second credential and a second failure mode.**
  Secret Manager's free tier is six active versions and the count was already
  tight; this consumes one. *(It was also "a second NFR-2 check" until
  2026-09-21 — that half is void, which makes a gateway cheaper than this ADR
  first assessed.)*
- ⚠️ **The fallback must be stated when the provider is chosen.** If the
  decision provider is unreachable, the dispatcher needs a defined behaviour —
  FR-15's deterministic signals already decide most tickets, and its
  below-threshold route is *plan it, and route to human review*, which is
  fail-safe. That is the shape of the answer, but it is not yet written into the
  design.
- **Nothing here blocks stage 0.** FR-15's deterministic signals cost nothing and
  run first; the classifier decides only the remainder.

## Revisit

### ⚠️ A FOURTH path, found 2026-09-21 and it partly reverses the exclusion

**OpenRouter can ENFORCE zero retention, which changes NFR-2 from something
checked to something guaranteed.** Verified against its own documentation: it
practises ZDR itself (prompts *"not retained unless you specifically opt in to
prompt logging"*), and enforcement is available at account level, per model
group, per API key as a guardrail, and per request via a `zdr` parameter that
ORs with the broader setting — so a request can only ever *ensure* ZDR, never
weaken it. Its default is conservative: an endpoint whose policy is unclear is
treated as retaining and training.

**That is a better mechanism than what FR-21 and FR-26 currently do.** Both
check NFR-2 compliance — at promotion and at substitution — by reading a
declared posture, which is to say by remembering. An enforced routing constraint
is mechanical, and it covers models nobody thought to declare.

⚠️ **But enforcement FILTERS a retaining provider, it does not make one
compliant.** So for Jev this is *checkable*, not resolved: either TypeSafe is
ZDR-eligible on OpenRouter and Jev becomes admissible, or it is not and this
ADR's exclusion stands — enforced automatically rather than remembered.

**Verified against OpenRouter's API 2026-09-21, because the docs pages are not
reliable here.** Jev IS routable: `typesafe/jev-1.13` and `~typesafe/jev-latest`
both resolve, modality `text->decisions`, 32k context, prompt $0.000000042/token
with completion free, 100% uptime over the sampled window.

⚠️ **`/api/v1/models` does NOT list it**, and that is a trap rather than an
answer: the catalogue returns 446 models across 61 providers and Jev is in none
of them, because that listing is chat-completions shaped and Jev's modality is
not `text->text`. Checking availability that way reports absent for a model that
is present — which it did here, and nearly caused this section to be retracted
as wrong when it was right. **Query the endpoints route for the specific id.**

⚠️ **PIN THE VERSION.** `~typesafe/jev-latest` floats — the `~` prefix is what
makes "latest" resolve at all, and `typesafe/jev-latest` without it 404s. A
floating id lets the model change with no configuration edit, which contradicts
FR-14's premise that the model IS configuration, and §3a's boring-and-stable
bias for anything unattended. Use `typesafe/jev-1.13`.

**ZDR eligibility: NOT VERIFIED, but the inference is strong and points to NO.**

Two API routes were tried. The **endpoints** route exposes no data policy at all
— its fields are purely operational, latency and uptime and throughput and
pricing. The **providers** route does list TypeSafe, and what it carries is the
answer's shape rather than the answer:

```json
{"name": "TypeSafe", "slug": "typesafe",
 "privacy_policy_url": "https://typesafe.ai/legal/privacy-policy",
 "terms_of_service_url": "https://typesafe.ai/legal/terms"}
```

**No ZDR flag — OpenRouter links the vendor's own policy instead.** And that
policy is the one already read for this ADR: no training on input, but **zero
retention is enterprise-only**, with the default permitting retention *"while
reasonably needed"*.

**So the chain closes by inference:** OpenRouter classifies endpoints from the
provider's stated policy, and its documented default is conservative — an
unclear or retaining policy is treated as retaining and training. TypeSafe's is
not unclear; it is clearly retaining by default. **Enforcing ZDR would therefore
filter Jev OUT, not admit it**, and this ADR's exclusion stands — now with a
mechanism behind it rather than an attestation.

⚠️ **This is an inference from two documents, not a test.** The one-minute
confirmation is still the account: enable ZDR enforcement and see whether
`typesafe/jev-1.13` still routes. Recorded as likely-no rather than no, because
the difference between those two is exactly the discipline that caught the
`/api/v1/models` trap above. **The authoritative check is
not a document**: enable ZDR enforcement on an OpenRouter account and see
whether Jev still routes. Given the conservative default, routing under
enforcement *is* the eligibility answer. (Same shape as the naming work's
finding that only the signup form settles a handle — the vendor's own
enforcement, not its prose, is the oracle.)

**Three earlier paths, none of them chosen here:**

1. **Exclude Jev and look again.** NFR-2 stands as written; the seam keeps
   waiting for a provider that clears both halves.
2. **Pursue ZDR.** An enterprise agreement for a workload costing $0.017/month
   is implausible, but it is the vendor's own stated route.
3. **Send less, so retention stops mattering.** FR-15 already computes
   deterministic signals for free. If the classifier received THOSE plus a
   redacted summary rather than raw ticket text, nothing retained would be the
   owner's work. It weakens the classifier, and **FR-15's held-out set is
   exactly the instrument for deciding whether it still beats the deterministic
   rules alone.** This is the only path that is testable rather than
   negotiable.

## Revisit

## Starting configuration, and the flexibility deliberately retained

**Start on OpenCode Go — the reason is budget, and it is a good one.** $10/month
flat against pay-per-token at ~1.06M tokens a ticket is a real discount, not a
rounding error, and the product is unproven. `engineering-defaults`' own
instruction is to start on the cheapest tier while the thing is a hypothesis.

**But OpenRouter-for-both-seams stays an admissible configuration**, and FR-14's
taxonomy already permits it without amendment: one key, one OpenAI-compatible
protocol, chat models for the build seam and a System One model for the decision
seam, with ZDR enforced across all of it. Recording it as admissible rather than
adopting it, because the cost case only inverts once per-token spend exceeds the
subscription — which is a measurement the run record will make.

| Revisit | When |
|---|---|
| The candidate | it is scored against FR-15's held-out set — the only gate left, since NFR-2 no longer bars anything |
| The build provider | per-token spend through a gateway would beat OpenCode Go's flat $10 — measurable from FR-6's cost field |
| ~~The NFR-2 mechanism~~ | **moot from 2026-09-21** — FR-21's and FR-26's posture checks were removed with NFR-2's rewrite; posture is recorded, not enforced |
| The seam itself | if a second classification appears — FR-9's unverifiable-fact and FR-10's fork detection are both bounded-output decisions the build model currently makes inline, spending its own context on them |
| The gateway | if more than one classifier is ever compared in production rather than in evaluation |
