# 0022 — A run's CI result is recorded once, on its row, by the dispatcher's poll

- **Date:** 2026-10-28
- **Status:** proposed — the run success card on Statistics (`design/inbox-desktop-v1.svg` panels 43-48, `design/inbox-mobile-v1.svg` panels 60-65) waits on it

## Context

The owner's standing goal is a 90% run success rate: the share of runs that end in a pull request whose required checks passed. A run row holds `outcome`, `pr_number` and `pr_url`, but no check result and no head sha of the pull request. Verified 2026-10-28 on all 69 finished records: `job_results` is empty on every one, and it holds only the runner's own jobs, "run" and "pr" (`internal/runner/record.go:261`).

GitHub is read for checks in two places, and neither fits. The surface reads a PR's check state live for an open PR's CURRENT head, as a display read (`internal/dispatcher/github.go:410`, `:444`), and `ChecksGreen(repo, sha)` reads one commit (`internal/dispatcher/github.go:305`). The goal is about what the AGENT delivered, so it must read the agent's last commit: PR 260 went green only after the owner's format fix.

What gates a merge was read from GitHub on 2026-10-28: the ruleset `protect-main` requires `go` and `web` and no review. The repository is public, so the check-run read needs no extra credential (`infra/README.md:520`).

## Decision

**Record the result once, on the row, when the checks settle; the gauge reads rows only.**

- The runner writes `pr_head_sha` when its PR job finishes: the agent's last commit.
- The dispatcher's existing 15-minute poll (`adr/0013`) reads the REQUIRED checks of that sha, the ruleset's contexts, and writes `checks_result` (`green` or `failing`) once every one is terminal. It is written once and never rewritten: a commit's checks do not change.
- Success is `outcome` = `pr-opened` and `checks_result` = `green`. The window is four weeks of runs by start time, read from rows as `adr/0016` reads them; a run whose result is still pending is not counted until it settles.
- The 50 existing pull requests are backfilled once, gated, taking the sha as the agent's last commit by author, as the rewrite measure identifies agent commits.

## Alternatives

| # | Option | Why not |
|---|--------|---------|
| 1 | Read checks live at page view, in batched GraphQL by sha | puts GitHub's latency and failures on the Statistics page, and its read cost scales with page views, not runs |
| 2 | Derive from the merge state the rows already hold (`rewrite_merged_at`) | measures the owner's action, not the agent's output: an owner fix turns red into merged, and an open PR reads as a failure |
| 3 | A GitHub webhook into the public webhook service | a new public route and signing secret to save at most 15 minutes, at about 19 runs a week |

## Revisit when

The repository goes private: fine-grained tokens have no Checks permission (`infra/README.md:520`), so read `statusCheckRollup` through GraphQL, as `PullSummaries` does. The window's rows pass `adr/0016`'s 200-row cap, which starts its ladder (cache header, rollup, BigQuery). The lag between a PR's checks finishing and the poll writing the result starts to matter, which is the webhook's case.

## Consequences

One GitHub read per pull request run, once, and about 70 bytes more on each row; the Statistics read adds `checks_result`, `outcome`, `started_at` and `stop_reason` to its projection. Rows from before the backfill read as unknown, never as failing.

## Open, for the ticket and the owner

- Whether `no-changes` and owner-cancelled runs count in the denominator.
- The minimum number of runs before a rate is stated: 20 is drawn, a judgment call.
- Required checks versus every check: counting every check would charge the agent for the review bot's own red, as on PR 259.
