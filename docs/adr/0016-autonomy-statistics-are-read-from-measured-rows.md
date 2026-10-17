# 0016 — The Autonomy statistics are computed at read time from measured rows

- **Date:** 2026-10-17
- **Status:** proposed — FRG-155 (page), FRG-156 (read path), FRG-157 (models on the row)

## Context

The Autonomy page shows the rewrite rate over windows of 10/5/1 PRs, per PR, by plan/build/review model, by size, per repository and as a trend. Every figure is a pure function of the measured runs (34 today, a few more per day). The owner asked for a store that answers as fast as possible.

## Decision

**Compute at read from one capped query; denormalize what the figures group by onto the row when it is measured. No rollup yet.**

- One query: measured rows newest first, capped at 200 (`internal/store/rewrites.go:99`). Windows, strip, by-model, by-size, per-repo and trend are computed from that result in memory; a page view is one query, ≤200 document reads.
- The measured row carries repo, size and the plan, build and review model, written when it is measured (FRG-157), so no figure joins another document. Models of past plan-staged PRs are backfilled once, gated.
- The query projects only the fields a figure reads, plus `steps` for the build and review models. Measured 2026-10-17 on 55 run documents: a full read was 850 KB, five fields 30 KB; `ticket_body` (23%) and `steps` (21%) carry the most bytes, so the page's cost is payload, not row count.
- Rates stay pooled lines, never a mean of rates; the pure functions are the single definition and the page computes nothing.

## Alternatives

Researched 2026-10-17 against the cloud already in use (GCP, native-first); Google's pricing was read, the vendor comparisons were not treated as evidence.

| # | Option | Why not now |
|---|--------|-------------|
| 1 | Firestore server-side aggregations | count, sum and avg only, billed per 1,000 index entries read; no group-by, median or p90, which the figures need |
| 2 | Stored rollup written when a PR is measured | a second source of truth to keep in step with the rows; nothing is slow enough to need it |
| 3 | BigQuery through the Firestore export extension (GCP-native) | **the revisit path**: realtime, incremental mirror of `runs`, SQL for median, p90 and group-by; runs a function per write and bills storage and queries separately (billing not verified here, read Google's pricing before adopting) |
| 4 | ClickHouse or another OLAP service | a second always-on service for 34 rows; vendor sources put its crossover at about 1 million to 100 million rows, and most are written by sellers |

## Revisit when

A projected page view is measurably slow on Cloud Run, measured rows pass the 200 cap, or the owner wants history beyond the cap or ad-hoc queries. The ladder, cheapest first:

| # | Step | Why this order |
|---|------|----------------|
| 1 | `Cache-Control: private, max-age` on `/api/autonomy`, with one read in flight at a time | the figures change only when a PR is measured, a few times a day, so a minute-old answer is harmless; an in-process cache would be cold after every scale-to-zero, so the header does the work (HTTP caching, RFC 9111; the max-age value is a judgment call) |
| 2 | a rollup document written when a PR is measured | the stored cache, rejected above until step 1 is measured to be too slow |
| 3 | BigQuery through the Firestore export, behind the same read function | history beyond the cap or ad-hoc queries; the page does not change |

Measure before adding a step: a cache adds staleness and invalidation rules the read does not have today.

## Consequences

A row missing a model reads as unknown, not guessed. FRG-156 stays as planned.
