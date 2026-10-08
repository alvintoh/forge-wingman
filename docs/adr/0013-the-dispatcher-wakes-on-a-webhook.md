# 0013 — The dispatcher wakes on Linear's webhook; the schedule stays as its backstop

- **Date:** 2026-09-24
- **Status:** accepted — built in Phase 3, with the dispatcher it wakes
- *Updated (2026-10-08, FRG-22 planning): the webhook also acknowledges the agent session~~, so it holds `linear-token`~~; the row is a marker keyed on the agent SESSION id, so a redelivery is a no-op while a new session on the same issue is acknowledged and woken; one run per issue stays guaranteed by the poll's `runs/<identifier>` create.*
- *Updated (2026-10-08, FRG-34): no Linear access token is stored. The webhook mints its own from the app's client credentials (`linear-client-id`, `linear-client-secret`), re-mints when Linear refuses it, and revokes it on shutdown; `linear-token` is deleted.*
- **Supersedes:** `tech-design-v1.md` §Two containers that deliberately do NOT exist,
  *"No inbound webhook receiver"*

## Context

**FR-1 accepts 15 minutes from delegation to queue, and the design polled to fit it.**
Delegation also fires a `created` `AgentSessionEvent` webhook, so a run can start in
seconds rather than up to 15 minutes. The owner chose performance.

**The webhook cannot replace the poll**, for three reasons that are each an event
nobody emits: Linear retries a failed delivery only 3 times (1 min, 1 h, 6 h) and
makes no delivery guarantee; a budget window reopening (FR-22) is a moment in time,
not a Linear event; and a priority edit is not a delegation.

## Decision

**Both wake the dispatcher, and only the dispatcher decides.**

```
Linear: you delegate a ticket
   │
   ▼  webhook (seconds)             poll every 15 min (backstop)
┌─────────────────────────┐        ┌─────────────────────────┐
│ webhook service         │        │ Cloud Scheduler         │
│ verify Linear-Signature │        │                         │
│ write a new-ticket row  │        │                         │
│ (skip if already there) │        │                         │
└────────────┬────────────┘        └────────────┬────────────┘
             │ wake                             │ wake
             ▼                                  ▼
          ┌─────────────────────────────────────────┐
          │ dispatcher (Cloud Run job)              │
          │ size, then select by Linear priority    │
          │ affordable within budget? claim it      │
          └────────────────────┬────────────────────┘
                               │ workflow_dispatch
                               ▼
          ┌─────────────────────────────────────────┐
          │ runner (GitHub Actions)                 │
          │ worktree → build → draft PR → record    │
          └─────────────────────────────────────────┘
```

1. **A fourth deployable: a small public Cloud Run service, in Go** — `cmd/webhook`,
   a third entrypoint in the one module, so `adr/0009`'s reasoning applies
   unchanged. It cannot sit behind IAP, since Linear cannot authenticate to it, so
   it verifies the HMAC-SHA256 `Linear-Signature` over the raw body and rejects
   anything else.
2. **It only enqueues and wakes.** Write a marker row keyed on the agent session id —
   a redelivery of the same session is a no-op, and the poll's own `runs/<identifier>`
   create keeps one run per issue when the poll delivers the same ticket — acknowledge the agent session with a `thought` (Linear marks a session
   unresponsive without one inside 10 seconds), then start a dispatcher execution, and
   return inside Linear's 5-second limit. Only `created` events act; it also rejects a
   `webhookTimestamp` older than 60 seconds, as Linear recommends. Sizing,
   selection and the budget guard stay in the dispatcher (`adr/0003`).
3. **The poll stays at 15 minutes.** FR-1's tolerance becomes the WORST case — a lost
   webhook — rather than the normal one.

*Established parts: events plus periodic reconciliation (a Kubernetes controller's
watch plus resync is the familiar form), and idempotent consumers under at-least-once
delivery. Choosing it for latency is the owner's call.*

## Consequences

- **A public attack surface exists**, bounded to one route that verifies a signature
  and writes one row. ~~It holds no credential beyond the store and the job trigger.~~
  ~~*Updated (2026-10-08):* it also holds `linear-token`, used only to post the
  session acknowledgement, and only after the signature verifies.~~
  *Updated (2026-10-08, FRG-34):* it also reads the Linear app's client
  credentials, to mint the token it posts the session acknowledgement with, and
  only after the signature verifies.
- ~~**Enqueue latency drops from ≤15 minutes to seconds**~~ *Updated (2026-10-08):*
  **enqueue latency drops from ≤15 minutes to about 2 minutes**: the job execution
  starts within seconds, but Cloud Run's gen2 start-up for a job takes ~2 minutes
  before the poll's own ~3 s (measured with the poll's phase timings); throughput does not change,
  since 96 polls a day already exceed level 5's 10 runs.
- **Two paths enqueue, so the enqueue must be idempotent** — the same property the
  claim transaction already has.
- **The tracer bullet (Phase 2) is unaffected**; it is triggered by hand.
