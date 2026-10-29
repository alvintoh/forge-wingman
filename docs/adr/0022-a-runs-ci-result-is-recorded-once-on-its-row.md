# 0022 — A run's CI result is recorded once, on its row, by the dispatcher's poll

- **Date:** 2026-10-28
- **Status:** proposed — the run success card on Statistics (`design/inbox-desktop-v1.svg` panels 43-48, `design/inbox-mobile-v1.svg` panels 60-65) waits on it
- *Updated (2026-10-29, owner's choice at planning):* the dispatcher's poll finds the agent's last commit ITSELF, by author login, from the pull request's commit list, and writes `pr_head_sha` and `checks_result` together. ~~The runner writes `pr_head_sha` when its PR job finishes.~~ Writing it from the PR job needs a new job output in `.github/workflows/run.yml`, which is the owner's file, and a second path for the backfill; the poll needs neither, and the same code settles old rows and new. Probed unauthenticated on 2026-10-29: `GET /repos/alvintoh/forge-wingman/rules/branches/main` answers 200 with `required_status_checks` `go` and `web`, and `GET /pulls/259/commits` shows the agent's commit `3ac55c4` authored `github-actions[bot]`.

## Context

The owner's standing goal is a 90% run success rate: the share of runs that end in a pull request whose required checks passed. A run row holds `outcome`, `pr_number` and `pr_url`, but no check result and no head sha of the pull request. Verified 2026-10-28 on all 69 finished records: `job_results` is empty on every one, and it holds only the runner's own jobs, "run" and "pr" (`internal/runner/record.go:261`).

GitHub is read for checks in two places, and neither fits. The surface reads a PR's check state live for an open PR's CURRENT head, as a display read (`internal/dispatcher/github.go:410`, `:444`), and `ChecksGreen(repo, sha)` reads one commit (`internal/dispatcher/github.go:305`). The goal is about what the AGENT delivered, so it must read the agent's last commit: PR 260 went green only after the owner's format fix.

What gates a merge was read from GitHub on 2026-10-28: the ruleset `protect-main` requires `go` and `web` and no review. The repository is public, so the check-run read needs no extra credential (`infra/README.md:520`).

## Decision

**Record the result once, on the row, when the checks settle; the gauge reads rows only.**

- The dispatcher's existing 15-minute poll (`adr/0013`) takes each run that opened a pull request and has no `checks_result`, takes the repository and number from the row's `pr_url` (a run's `pr_number` is written only at merge), and finds the agent's last commit: the last commit of the pull request whose GitHub-resolved author login is the runner's, as the rewrite measure identifies agent commits.
- It reads the REQUIRED checks of that sha, the ruleset's contexts, and writes `pr_head_sha` and `checks_result` (`green` or `failing`) together, once every one is terminal. They are written once and never rewritten: a commit's checks do not change. A required check that has not reported keeps the run pending, and a run still pending after 24 hours is logged, not counted.
- Both fields live beside the run record, not inside it, because the record is rebuilt on every finalize (`internal/store/rewrites.go:19-24`'s rule), and a requeue clears both with the other settled-run fields (`internal/store/queue.go:1511-1521`).
- Each poll settles at most 20 runs, oldest first, so the 50 existing pull requests are backfilled by the same code over a few polls and no separate backfill command exists.
- *Updated (2026-10-29, owner's choice after review):* a run the poll can never settle is written `checks_result` = `unresolved` once, with no `pr_head_sha`: a pull request with no commit authored by the runner's login (an owner squashed it or force-pushed over the agent's commits) or a `pr_url` that does not parse. Without it such rows keep the front of the oldest-first list forever and starve newer runs under the 20-per-poll cap. A pending run or a failed read is never marked. The rate leaves `unresolved` runs out of both its numerator and its denominator, and a run cancelled in GitHub counts as a failure for now (one in 69 runs; excluding it needs a new field).
- Success is `outcome` = `pr-opened` and `checks_result` = `green`. The window is four weeks of runs by start time, read from rows as `adr/0016` reads them; a run whose result is still pending is not counted until it settles.

## Alternatives

| # | Option | Why not |
|---|--------|---------|
| 1 | Read checks live at page view, in batched GraphQL by sha | puts GitHub's latency and failures on the Statistics page, and its read cost scales with page views, not runs |
| 2 | Derive from the merge state the rows already hold (`rewrite_merged_at`) | measures the owner's action, not the agent's output: an owner fix turns red into merged, and an open PR reads as a failure |
| 3 | A GitHub webhook into the public webhook service | a new public route and signing secret to save at most 15 minutes, at about 19 runs a week |
| 4 | The runner's PR job writes the sha (this ADR's first draft) | needs a new output in `.github/workflows/run.yml` and a record-job flag, and the 50 existing rows still need a second path; the poll resolves it from the pull request with neither |

## Revisit when

The repository goes private: fine-grained tokens have no Checks permission (`infra/README.md:520`), so read `statusCheckRollup` through GraphQL, as `PullSummaries` does. The window's rows pass `adr/0016`'s 200-row cap, which starts its ladder (cache header, rollup, BigQuery). The lag between a PR's checks finishing and the poll writing the result starts to matter, which is the webhook's case.

## Consequences

About three GitHub reads per unsettled pull request per poll (its commit list, the required contexts, its check runs), none once settled, capped at 20 runs a poll; about 70 bytes more on each row; the Statistics read adds `checks_result`, `outcome`, `started_at` and `stop_reason` to its projection. Rows from before the backfill read as unknown, never as failing.

## Open, for the ticket and the owner

- Whether `no-changes` and owner-cancelled runs count in the denominator.
- The minimum number of runs before a rate is stated: 20 is drawn, a judgment call.
- Required checks versus every check: counting every check would charge the agent for the review bot's own red, as on PR 259.
