# 0011 — GCP-native observability, and a liveness alert for the dispatcher

- **Date:** 2026-09-22
- **Status:** accepted

## Context

**The error-tracking choice existed only as a row on a diagram.** `gen_architecture.py`
put *"Sentry — per defaults, day one"* against the dispatcher and the runner, while
`tech-design-v1.md` mentioned it **zero** times and none of `adr/0001`–`0010` named it.
So a vendor was selected with no recorded reasoning, nothing testing it, and no ticket
carrying it — found on 2026-09-22 by rendering the diagram and reading it.

**And the larger half was missing entirely.** NFR-4 requires *"observability precedes
optimisation"*, FR-23 defines a systemic-failure notice over five stop classes, and
between them they cover **failures the dispatcher records**. Nothing covers the
dispatcher **not running**.

⚠️ **That is not a gap in FR-23; it is outside it by construction.** Its trigger is
*"When the dispatcher records it"* — so a notice emitted BY the failing component
cannot report that component's absence. FR-23's own reasoning already names the
consequence for a different case: *"Silence for a month is exactly the failure this
requirement exists to prevent."* A Cloud Scheduler job that stops firing produces that
same silence, and throws nothing for an error tracker to catch.

## Decision

**1. Cloud Logging and Error Reporting, not Sentry.** The config repo's two gates, in
order — is the native option *sufficient*, and is it *free*:

| | GCP-native | Sentry |
|---|---|---|
| sufficient | yes at this shape — one reader, ~10 runs/day | yes, and better at grouping, releases, traces |
| free | yes, and already in-perimeter | account, DSN, an SDK dependency |

**Sentry fails the first gate rather than the second**: it is not *insufficient*, it is
unnecessary. Its advantages scale with a team triaging volume, which is the one thing
this product does not have. Revisit if a second reader appears, or if grouping across
releases becomes the bottleneck.

**2. A Cloud Monitoring alerting policy on metric ABSENCE for the dispatcher.** Not an
error rate and not a log match — an absence condition on the job's execution count,
firing when no execution is recorded inside a window derived from FR-1's ≤15-minute
schedule. It lives **outside** the dispatcher, which is the whole point.

**3. The chat webhook stays FR-23's, and the alert is a second channel.** Routing by
who must act: FR-23's link-only notices are the error-tracker tier — eventual triage,
deliberately not urgent. A dispatcher that has stopped is the urgent tier. Folding them
into one channel would make the thing FR-23 spent an argument avoiding: a channel
ignorable within a fortnight.

## Consequences

- **NFR-7's stdout prohibition becomes load-bearing for a SECOND reason.** It already
  forbids completions on a runner's stdout, stderr or build log because a public
  repository's log is *"world-readable permanently"*. Cloud Logging captures stdout, and
  carries **its own retention that NFR-7 does not control** — so a completion leaking
  there escapes the 90-day bound the same way a build log would. The prohibition was
  written against build logs; it now guards the log sink too, and that is not obvious
  from either document alone.
- **The runner's log stays phase progress and identifiers only**, per NFR-7. Adopting a
  log sink changes nothing about what may be written to it.
- **The absence window is derived, not chosen.** FR-1 sets ≤15 minutes, so the window
  must exceed one interval and stay well under the point where a day's runs are lost.
  The exact figure is tuning, and NFR-4 forbids shipping a tuning mechanism before the
  run record that would measure it — so it starts conservative and is measured.
- **`adr/0008` already provisions the alerting policy's home.** OpenTofu before the
  first deploy means this is reviewable IaC rather than console state, which is the same
  argument `0008` makes for the GCS lifecycle rule enforcing NFR-7.
- **The diagram must be regenerated, not edited.** Its `errors` rows named Sentry; they
  now name the native pair, and the absence alert appears as its own row rather than
  being implied by the presence of error tracking.
- **What this does NOT decide:** tracing. `engineering-defaults` records Langfuse for AI
  work and it is OTEL-native, so it remains reachable later. Nothing here needs it, and
  NFR-4 says the measurement precedes the mechanism.
