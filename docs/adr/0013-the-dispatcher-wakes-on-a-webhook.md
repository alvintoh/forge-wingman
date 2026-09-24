# 0013 — The dispatcher wakes on Linear's webhook; the schedule stays as its backstop

- **Date:** 2026-09-24
- **Status:** accepted — built in Phase 3, with the dispatcher it wakes
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
2. **It only enqueues and wakes.** Write a new-ticket row keyed on the issue id —
   a duplicate write is a no-op, because the poll can deliver the same ticket — then
   start a dispatcher execution, and return inside Linear's 5-second limit. Sizing,
   selection and the budget guard stay in the dispatcher (`adr/0003`).
3. **The poll stays at 15 minutes.** FR-1's tolerance becomes the WORST case — a lost
   webhook — rather than the normal one.

*Established parts: events plus periodic reconciliation (a Kubernetes controller's
watch plus resync is the familiar form), and idempotent consumers under at-least-once
delivery. Choosing it for latency is the owner's call.*

## Consequences

- **A public attack surface exists**, bounded to one route that verifies a signature
  and writes one row. It holds no credential beyond the store and the job trigger.
- **Enqueue latency drops from ≤15 minutes to seconds**; throughput does not change,
  since 96 polls a day already exceed level 5's 10 runs.
- **Two paths enqueue, so the enqueue must be idempotent** — the same property the
  claim transaction already has.
- **The tracer bullet (Phase 2) is unaffected**; it is triggered by hand.
