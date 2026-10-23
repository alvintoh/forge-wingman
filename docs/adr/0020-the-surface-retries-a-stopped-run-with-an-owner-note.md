# 0020 — The surface retries a stopped run with the owner's note

- **Date:** 2026-10-23
- **Status:** proposed — built: the retry route, the inbox's stopped rows and the page; the live check on dev is still to run

## Context

A run that stopped, or opened a draft the owner would rather not review, can today only be sent again from a shell (`runner requeue`), and the new attempt knows nothing the owner has since learned. FR-24 and FR-25 ask for a RETRY action that carries a written note into the next attempt, as the surface merges (`adr/0017`): the page acts, the server decides.

One row per ticket stays (`runs/<ticket>`, the run id is the ticket id). A retry therefore overwrites the ended attempt's row, and the attempt's stop reason, PR and note would be lost without a copy.

## Decision

- **`POST /api/runs/{run}/retry` with `{note, expected_attempt}`.** The note is required: the action is disabled until there is one. `expected_attempt` is the attempt the owner saw (`attempt` on the inbox row, counted from 0), so a run that has moved on is refused with `attempt-moved`. The server derives the repository, the PR and the actor itself.
- **The actor is the IAP identity**, from the signed assertion, never a body field; a request that skipped the gate is refused with 401 and writes nothing. It is stored as `retried_by`, the first handler to record who acted.
- **The ended attempt is archived first**, in the same transaction that queues the new one: the whole row is copied to `runs/<ticket>/attempts/<n>` with `superseded_by_attempt` and `archived_at`. `n` is the ended attempt counted from 0, as the row's `attempt`, the ledger keys (`#a<n>`) and `superseded_attempt` are, so every record names an attempt the same way; the page may print `n + 1`.
- **The retry re-plans.** The row returns to `queued`, as a rebase-conflict requeue and a decision supersede do, not to `planned`: a note can widen the write set, and a stale plan would hold the new PR as outside its plan. The cost is one plan stage per retry.
- **The note is a criterion for that attempt alone.** It is stored on the row beside the record (`operator_note`, `operator_assisted`, `retried_by`), outside `runner.Record`, so `Finalize` leaves it. The ticket job appends it to the ticket text under "Owner's note for this attempt", which reaches the plan, the build and the review; the rules head and `rule_stack_sha` are untouched. A requeue the owner or a decision makes drops all three fields; the automatic requeues of a retry (a rebase conflict, a transient fault) keep them, since they are the same attempt tried again.
- **A draft PR is replaced by the existing supersede path.** When the row names a PR, the surface reads it from GitHub and requires it to be an open draft (`pr-ready` means merge it instead, `pr-merged` and `pr-closed` are final); the retry records its number as `superseded_pr`, the ended attempt as `superseded_attempt` and, beside them, `superseded_by_retry`, the fields a decision supersede writes (which clears the last). The new attempt's pr job closes the old draft after its own PR opens, so a retry that stops early leaves the old draft standing. pr-meta words the PR body's supersede line, and the close comment through the output `superseded_by_retry`, from the row's `superseded_by_retry`, which lives exactly as long as the `superseded_pr` it explains, so a later requeue that drops the note does not re-word a PR the row still names. A run with no PR makes no GitHub call.
- **The inbox lists stopped runs that left no PR**, through the predicate `StuckRuns` already uses for a finished claim and a stopped row, with these left out: a run that made no changes, an abandoned one, one claimed again since it settled, a merged one, and one a poll will requeue by itself (a known transient fault with automatic requeues left), which would appear and vanish within a poll. Each row carries `attempt`, `outcome`, `stop_reason` and `retryable`, computed in Go as `wait_reason` is.
- **A repeat of an applied retry** (same attempt, note and actor) answers 200 `already-retried` and moves nothing.

## Consequences

- **`retried_by` is not tamper-proof against the runner.** It lives in `(default)`, which the runner's identity can write exactly as it can `merged_by_owner_at` (`adr/0017`, `adr/0019`); the requeue also touches the ledger document, so it cannot move to the `owner` database. "Recorded as the owner" means recorded from the IAP identity by the surface, by no runner code path.
- **The surface's Firestore grant is unchanged** (`roles/datastore.user`, project-wide): it already writes run rows, the ledger and subcollections, so only its description changes.
- **A retry does not wake the dispatcher.** The run starts at the next poll (10 to 15 minutes), and the page says so.
- **The Linear ticket is not moved.** The surface holds no Linear credential, so a retried ticket stays in the state it was in until the next claim starts it.
- **A stale record job can still overwrite a retried row.** A rerun of the ended attempt's record job writes its `Record` fields over the new attempt's through `PutRecord`'s merge. That is true of every requeue and is not widened here.
