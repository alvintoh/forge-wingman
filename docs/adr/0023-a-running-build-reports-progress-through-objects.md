# 0023 — A running build reports progress through objects, never the run row

- **Date:** 2026-11-05
- **Status:** accepted — FRG-296 builds it, once its surface is drawn.

## Context

A run row is written when a run is claimed and again when the record job ends, so for the whole
run cap a stalled build and a slow one look the same: on FRG-164 the row's last write was the claim,
and GitHub's job log returns 404 until the step ends. Only the record job may write the run store.
The model job is barred from it by the same job boundary that withholds marking a PR ready or
merging (tech design, *Gates are enforced in three places*), so the build cannot update its own row.
The model identity already holds `roles/storage.objectCreator` on the completions bucket
(`infra/identity.tf`, `model_completions`), which creates objects and cannot overwrite them.

## Decision

- **The runner process in the model job, never the agent, creates `progress/<attempt>/<seq>.json`
  in the completions bucket every 3 minutes**: the phase, the step count, the diff lines added and
  removed, and the time it was written. Each write is a new object, which is all create-only access
  allows, so the model job gains no permission.
- **Updated (2026-11-05), from grounding:** ~~`progress/<attempt>/<seq>.json`~~ the object is
  `progress/<run row>-a<attempt>-<stage>/<unix seconds, 10 digits>-<GitHub run attempt>.json`.
  The surface knows an in-flight run only by its run row, attempt and stage (the ledger
  reservation), so those name the prefix, and the workflow passes them to the model job; a
  sequence number would collide on a GitHub re-run, since every write is create-only, while the
  zero-padded time sorts newest last.
- **A write that fails is logged as `progressWriteFailed` and never fails the build.**
- **The surface gets read access to the completions bucket limited to objects under `progress/`**
  by an IAM condition, reads the newest object for an in-flight run, and treats every field as
  untrusted: a bounded integer, a known phase or none. It writes the progress nowhere.
  **Updated (2026-11-05):** the age is the object's creation time in Cloud Storage, read on the
  server, never the time the body states, so a skewed or false runner clock cannot make a run
  look fresh.
- **The run row stays the record job's alone**; the record job ignores progress objects. The
  bucket's 90-day lifecycle rule already deletes old ones.

## Rejected

- **Granting the model job write access to the run store**: it hands back the capability the job
  boundary withholds.
- **GitHub's job-steps API**: it gives elapsed time per step only, with no step count or diff.

## Consequences

- A new data class in an existing store, and one new, prefix-limited read grant for the surface.
- **Added (2026-11-05):** the model identity may create any object name in the bucket, so a
  build could fake another run's progress or take its next name first. Progress is display
  only and a failed write never fails a build, so the worst case is a misleading or missing
  caption; nothing decides on it.
- FRG-296's "writes its step count and diff lines to the run row" becomes "writes a progress object
  the surface reads".
- Where the surface shows a running build's progress needs a `/proto-draw` drawing before FRG-296
  is built.
