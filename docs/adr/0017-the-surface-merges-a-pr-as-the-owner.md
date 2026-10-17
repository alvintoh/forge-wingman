# 0017 — The surface merges a PR as the owner, behind a confirm and a head guard

- **Date:** 2026-10-17
- **Status:** proposed — the surface's merge action is not built; it needs its own ticket

## Context

FR-20 lists "act on the resulting pull request" as a later phase and says the surface's GitHub credential is read-only until then. The inbox now needs **MERGE PR** as its main action for every PR that will not merge itself: a front-end change, size L, a PR selected into the 20% sample, any PR while auto-merge is off (it is on at a trailing-10 rewrite rate of 10% or less and no reverted auto-merged PR, and the owner has not switched it off: `adr/0018`).

## Decision

- **`POST /api/runs/{run}/merge` with `{expected_head_sha}`.** The server derives the repository and PR number from the run row, never from the client; the client names only the head it saw.
- **Refused unless** the PR is open and not a draft, its checks are green and no provisional decision is unconfirmed. GitHub is the backstop: the merge call returns 405 when it cannot merge and 409 when the head is not the `sha` it was given (GitHub's merge-a-pull-request reference, read 2026-10-17).
- **Squash, with the `sha` guard.** The merge call does not say it deletes the branch; `alvintoh/forge-wingman` has `delete_branch_on_merge` on (read 2026-10-17), so the dialog's "delete the branch" is true there. A repository without that setting needs an explicit delete before it is allowlisted for this action.
- **A confirm step is required, not decoration.** FR-8 binds whoever acts (FR-20), so the drawn BACK / MERGE PR dialog stands between the owner and the call.
- **The inbox row carries `wait_reason`**, computed in Go: `front-end`, `size-l`, `sampled`, `auto-merge-off`, `unconfirmed-decision`, `draft`, `checks-not-green`. The first four explain why the PR did not auto-merge and never block the owner; the last three block the button and are shown beside it.
- **Credential:** a fine-grained token scoped to the allowlisted repositories with **Contents: write** (the only permission GitHub lists for merging, verified 2026-10-17) and Pull requests: read, held in Secret Manager and readable by the surface's service account alone, never by a run job. Residual risk: Contents: write can also push; the surface exposes only the merge call and IAP admits one operator (`adr/0007`).
- **Recorded on the row:** `merged_by_owner_at` and the merged sha. A PR already merged answers success, not an error.

## Alternatives

| # | Option | Why not |
|---|--------|---------|
| 1 | merge in GitHub's own UI (today) | no action in the inbox; the owner leaves the page for the one action it exists for |
| 2 | a GitHub App installation token | the same Contents scope; a narrower one does not exist for merging |

## Consequences

The withheld capability stays withheld from the runner: only the surface service holds a token that can merge, and it acts on the owner's click. The tile-arrow figures and the inbox's `wait_reason` are computed in Go, so the page decides nothing.
