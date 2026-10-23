# 0021 — Cancelling a waiting run records it, and GitHub is left alone

- **Date:** 2026-10-23
- **Status:** accepted — the inbox row sheet offers it as Cancel run

## Context

The drawn row sheet (`design/inbox-desktop-v1.svg`, panel 02) offers "Cancel run: stop and
record, no partial action", and the inbox has no way to drop a run whose pull request the owner
will not take: every row waits for a merge. The surface's GitHub token is read-only on pull
requests (`adr/0017` scopes it to Pull requests: read beside Contents: write), so closing,
commenting on or marking the PR is not a capability the surface holds. What "no partial action"
means for an open pull request was decided by the owner on 2026-10-23: leave it.

## Decision

- **`POST /api/runs/{run}/cancel`, with an empty JSON body.** The run comes from the path and the
  owner from the IAP assertion, never from the request body; a request without one is refused with
  401 and writes nothing. The body carries no field, but the JSON content type is what a browser
  cannot send cross-origin without a preflight, so it keeps a page that is not the surface from
  cancelling a run with the session cookie IAP establishes.
- **Recorded on the run row:** `cancelled_by_owner_at` and `cancelled_by`, the signed-in address.
  `Finalize` carries both through a re-run of the record job; a requeue — the retry of FR-24 —
  clears them, because the run is live again.
- **Only a run whose pull request waits on the owner is cancelled.** One that is still building, or
  stopped short of a PR, is refused with 409 `not-waiting` and nothing is written: stopping a
  workflow is not something this surface does.
- **The inbox leaves the row out**, so the cancel is what stops the run waiting on the owner, and
  the run history draws it as "cancelled by you" rather than "PR open, waiting on you".
- **Nothing on GitHub changes.** The pull request is not closed, commented on or marked, and the
  run's auto-merge state is left as it is.
- **A cancelled run cannot then be merged:** `POST /api/runs/{run}/merge` refuses it with
  `cancelled`, so the cancel stops the run rather than hiding it from one list.
- **An already-merged or already-cancelled run answers success** with the status that says so and
  writes nothing, so a second request is not a second cancel. Merged is answered first, and it
  counts a merge by the owner through the surface and one the dispatcher's poll measured, because
  the run history draws either as merged.

## Alternatives

| # | Option | Why not |
|---|--------|---------|
| 1 | Close the pull request | the surface's token has Pull requests: read, so it cannot; the owner may also want the branch |
| 2 | Withdraw the pull request's auto-merge request | also a pull-request write; the runner withdraws it on a push (`automerge-withdraw.yml`) when the branch moves |
| 3 | Delete the run's row | that loses what was cancelled and by whom, which the record is for |

## Consequences

- A cancelled run's pull request is still open on GitHub, so it is closed or merged by hand; the
  surface offers no action on it. Until then it counts as an open agent PR toward the dispatcher's
  review WIP limit (`internal/dispatcher/github.go`, `OpenAgentPRs`), so a cancel does not free a
  slot for the next run.
- The cancel needs no new credential: it writes one Firestore document, as the merge records its
  own, and it is owner-only behind IAP (`adr/0007`).
- A cancelled run can be built again only by a retry, which clears the cancel; there is no
  un-cancel short of that.
