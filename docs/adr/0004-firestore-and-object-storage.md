# 0004 — Run records in Firestore, completions in Cloud Storage

- **Date:** 2026-09-21
- **Status:** accepted

## Context

`engineering-defaults` records Postgres as the store — *"Personal → Cloud SQL +
pgvector + Managed Connection Pooling"* — and separately warns that a
platform-hosted Postgres *"ties you to their platform for no gain when RDS/Cloud SQL
already own that role — fine for side uses, just not the system of record."*

**Cloud SQL is not in GCP's Always Free list** and carries an always-on floor.
Forge Octant reached the same wall and recorded it in its own `adr/0001` (verified
2026-09-16). Against NFR-1's USD 20/month *cash* ceiling with a $10/month provider
already committed, that floor is most of the remaining headroom.

FR-6 stores two things with nothing in common:

| | Shape | Volume | Access |
|---|---|---|---|
| the run record | structured, queried, filtered, aggregated | ~300/month — see below | read constantly |
| the **completions** | write-once opaque text | ~60 MB/month | read rarely, by run |

A scale-to-zero Postgres was evaluated and **rejected on measurement**, not on the
recorded warning: its free tier caps storage at 0.5 GB with a **hard cap that blocks
writes**, which ~60 MB/month of completions reaches in about eight months — the
dispatcher would stop being able to record anything. Its compute allowance is also
coupled to the poll interval, since a 5-minute idle suspend against a 15-minute poll
leaves the instance awake a third of the time; tightening FR-1 would exhaust it.

⚠️ **Both measurements above are WRONG, and the section below replaces them.** The
storage one is measured against a store holding completions — which this record's own
decision moves to Cloud Storage — and the compute one is arithmetically wrong at FR-1's
actual interval. The decision survives on different reasons; see *Reopened*.

### Reopened 2026-09-21 because it looked like it decided the LANGUAGE

*While `adr/0009` was being settled, Firestore's data plane looked like the one
capability separating Go from Rust — and every component touches it. So this record
was reopened to check whether the store was silently carrying a language decision it
had never been asked to make.*

**First finding — it was not, and the coupling dissolved from the other end.**
The apparent gap was one well-maintained community crate rather than an absence,
so **Firestore never ruled Rust out** and the store was never carrying the language
decision. `adr/0009` went on to settle the language on workload fit and ecosystem
cost, with this store unchanged either way. **Picking a store to unlock a language
would have been reasoning backwards** — which is the trap this reopening was
checking for, and it was not being sprung.

**Second finding — judged on its own merits, both rejections in the Context are
defective, and the decision still stands.**

| The rejection as written | Why it is wrong |
|---|---|
| storage: 0.5 GB reached in ~8 months | measured against a store holding the **completions** — which this record's own decision moves to Cloud Storage. The comparison was Firestore *with* the split against Postgres *without* it. Records alone are ~2 KB x ~300/month ≈ **7 MB/year**; 0.5 GB is decades |
| compute: the allowance would be exhausted | Neon's free plan is **100 CU-hours per project per month**, ≈400 hours at 0.25 CU (verified 2026-09-21). A 5-minute suspend against FR-1's **15-minute** poll is ~243 hours awake; the surface and the runner add ~50. **~293 of 400 — it fits** |

**What actually decides it is HEADROOM and the FAILURE MODE, and the original
reasoning named neither.**

- **Headroom.** Firestore's Always Free is 50,000 reads and 20,000 writes *per day*
  against ~300 writes a **month** — three orders of magnitude. Neon fits with ~27%,
  and that margin is **coupled to a requirement the owner has already said they intend
  to tighten**: the standing preference is to tune a cadence aggressively at first and
  relax it once stable. A 5-minute suspend leaves no idle gap below a ~10-minute poll,
  and an always-awake instance is 182 CU-hours against 100. **FR-1's tolerance is 15
  minutes and the tuning preference points straight at the floor.**
- **The failure mode when it goes.** Neon suspends the compute **until the next
  billing period**. For an unattended dispatcher that is a multi-day outage, and
  FR-23's systemic-failure notice would be firing into the store it can no longer
  write to. Firestore's overrun throttles one operation.

**Two real things Postgres would have bought, recorded so the cost is honest:**

1. **`GROUP BY` for FR-20's aggregates** — which the Consequences below already call
   *"the real cost of the decision"*, 50-100 lines of application code that SQL would not need.
2. **`SELECT … FOR UPDATE SKIP LOCKED` for `adr/0003`'s claim**, the textbook pattern,
   rather than a conditional transaction written by hand.

Both are genuine, and neither is worth a free tier that **fails closed** and sits
outside the owner's stated cloud.

⚠️ **The ~300/month figure is UNRECONCILED with `prd-v1`'s constraints and
`adr/0002`, which both say ~100** *(found 2026-09-21)*. It is left as-is here
because **nothing in this record turns on it** — at either volume Firestore has
three orders of magnitude of headroom, so the store decision is unchanged. **It
is not harmless elsewhere**: `adr/0002` shows it is the multiplier on the only
infrastructure line that can breach NFR-1, and it is recorded as an open range
there pending measurement.

## Decision

**Split by data class.**

- **Run records → Firestore.** 1 GiB storage and 50,000 reads / 20,000 writes *per
  day* free (verified 2026-09-21 against Google's Always Free page), against ~300
  writes/month. Transactions give `adr/0003`'s conditional claim directly.
- **Completions → Cloud Storage**, one object per run, with a lifecycle rule:
  condition `age: 90`, action `Delete` (verified 2026-09-21).

## Consequences

- **NFR-7 becomes configuration.** A lifecycle rule cannot be forgotten the way a
  scheduled delete job can, and there is no code path to review. **Soft delete is
  disabled on the bucket** — the default retains deleted objects for 7 days, which
  would make a 90-day privacy bound 97 days.
- **The split keeps the store writable.** This is the load-bearing consequence:
  completions in the record store would eventually block writes on any free tier.
- **No connection pool, and the Cloud Run exhaustion problem does not arise.**
  `engineering-defaults` devotes a section to instance-count × pool-size against one
  `max_connections`; Firestore has no connection to pool.
- **Only the GROUPED aggregates become application code.** ⚠️ *Corrected
  2026-09-21: this said Firestore has no aggregation at all, which is false.* It has
  server-side `count()`, `sum()` and `avg()`, billed as about one read however many
  documents match — so **spend per window is a single aggregation query**, which
  matters because FR-22 runs it on every poll. What Firestore lacks is `GROUP BY`, so
  the two *grouped* views — rewrite rate by model over time, and stage progression —
  read the window and fold it in application code: ~900 documents for a 90-day chart,
  ~50-100 lines, cacheable. Runs by gate class is six `count()` queries. **That
  remains the real cost of the decision; it is just smaller and narrower than first
  recorded.**
- **If aggregation ever outgrows that**, BigQuery is 1 TiB of queries and 10 GiB of
  storage free per month and Firestore exports to it. Named so the escape is known;
  a third store is not warranted at this volume.
- **Completions are never proxied through the surface** — it issues a short-lived
  signed URL and the browser reads the bucket directly.
- **Revisit trigger, added 2026-09-21 when this was reopened:** the FR-20 aggregate
  code outgrows a single file *and* the BigQuery export named above does not cover
  it, or the run record grows relations that need joins. **Not the language** — that
  is settled independently above, so a future Rust argument is not a reason to
  reopen this record.
