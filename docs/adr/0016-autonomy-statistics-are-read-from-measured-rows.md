# 0016 — The Autonomy statistics are computed at read time from measured rows

- **Date:** 2026-10-17
- **Status:** proposed — FRG-155 (page), FRG-156 (read path), FRG-157 (models on the row)

## Context

The Autonomy page shows the rewrite rate over windows of 10/5/1 PRs, per PR, by plan/build/review model, by size, per repository and as a trend. Every figure is a pure function of the measured runs (34 today, a few more per day). The owner asked for a store that answers as fast as possible.

## Decision

**Compute at read from one capped query; denormalize what the figures group by onto the row when it is measured. No rollup yet.**

- One query: measured rows newest first, capped at 200 (`internal/store/rewrites.go:99`). Windows, strip, by-model, by-size, per-repo and trend are computed from that result in memory; a page view is one query, ≤200 document reads.
- The measured row carries repo, size and the plan, build and review model, written when it is measured (FRG-157), so no figure joins another document. Models of past plan-staged PRs are backfilled once, gated.
- Rates stay pooled lines, never a mean of rates; the pure functions are the single definition and the page computes nothing.

## Alternatives

- **Stored rollup** written when a PR is measured: a second source of truth to keep in step with the rows, and nothing here is slow enough to need it. Rejected for now.

## Revisit when

Measured rows pass the 200 cap, or page views per day exceed PRs measured per day by a wide margin. Both thresholds are a judgment call, not established practice; the read-model pattern itself (CQRS) is.

## Consequences

A row missing a model reads as unknown, not guessed. FRG-156 stays as planned.
