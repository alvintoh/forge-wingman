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

**No broker. The run record in Firestore is the queue**, and selection is a query:

```
state == "queued"  ORDER BY linear_priority  LIMIT 1
```

The claim is a **Firestore transaction** conditioned on the row still being
`queued`, so two overlapping polls cannot dispatch one run. No document returned
means another poll won — the same read-then-conditional-write discipline a lease
needs, with the store enforcing it.

## Consequences

- **FR-22 becomes a `WHERE` clause** rather than a component. A deferral is a
  recorded field, and the next poll re-reads it with no redelivery to configure.
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
