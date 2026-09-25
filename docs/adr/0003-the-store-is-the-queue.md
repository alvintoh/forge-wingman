# 0003 — No message queue; the store is the queue

- **Date:** 2026-09-21
- **Status:** accepted

## Context

`engineering-defaults` records *"Pub/Sub → Cloud Run jobs (personal)"* as the queue
backbone for async work, with a dead-letter topic and bounded retry on every
consumer.

FR-22 describes something a message queue cannot express. The dispatcher
*"considers eligible tickets in **Linear priority order**, dispatches only if the
run's estimated cost would breach no ceiling, and otherwise records a deferral
**naming the binding ceiling** and reconsiders at the next poll."*

Three properties, none of which a broker offers:

- **Priority ordering that can change between polls.** Linear priority is editable
  at any time, so the ranking is a property of the source, re-read each cycle.
- **Indefinite deferral without redelivery semantics.** A budget-blocked run is not
  a failed delivery; it is an eligible row that is not yet affordable.
- **Re-ranking of the whole eligible set**, every poll.

Pub/Sub has no priority ordering; Cloud Tasks can schedule but not re-rank. Either
would be worked around rather than used, and the workaround is a queue whose
contents are re-published on every poll.

## Decision

> [!NOTE]
> **Updated (2026-09-25) for FR-27, concurrent dispatch by default.** Selection was
> `… LIMIT 1`, one run per poll. It is now an **admission loop** over the same query,
> and each claim also books the run's reservation. The decision itself — no broker,
> the store is the queue — is unchanged.

**No broker. The run record in Firestore is the queue**, and selection walks it in
priority order:

```
state == "queued"  ORDER BY linear_priority
  → admit each run whose FR-22 and FR-27 conditions hold, until N runs are in flight
```

Each claim is a **Firestore transaction** that reads the run row and a single
`dispatch/ledger` document, and commits only if the row is still `queued` and the
ledger's reserved total plus this run's estimate fits every ceiling. It flips the
row to claimed and adds the reservation in one write, so two overlapping polls can
neither dispatch one run nor jointly overbook a window. No document returned means
another poll won — the same read-then-conditional-write discipline a lease needs,
with the store enforcing it. The record job settles the reservation to actual
cost when the run ends.

**The ledger also holds N**, the discovered concurrency limit (FR-27's AIMD rule),
so the value that governs admission lives beside the reservations it bounds and
changes in the same transactional store.

## Consequences

- **FR-22 and FR-27 become an admission loop over one query** rather than a
  component. A deferral is a recorded field naming the binding condition, and the
  next poll re-reads it with no redelivery to configure.
- **The ledger serialises claims**, deliberately: every claim transaction touches
  one document. At this product's volume that contention is invisible, and it is
  what makes a reservation sum exact rather than approximate.
- **One fewer deployable, one fewer thing with its own failure modes.**
- **The dead-letter requirement is answered by requirements already present.**
  FR-13 bounds retry at one escalation; a second failure terminates with a record
  naming both attempts. The run record *is* the dead-letter store, and unlike a DLQ
  it is queryable — which is what FR-20's surface reads.
- **Polling is the cost.** A broker would push; this wakes on a schedule and finds
  nothing most of the time. FR-1's 15-minute tolerance is what makes that
  acceptable, and it is a requirement rather than a compromise.
- **This does not generalise.** A queue with many producers, fan-out, or work whose
  order is fixed at enqueue time wants a broker. The deciding property here is that
  *priority is owned by an external system and mutable*.
