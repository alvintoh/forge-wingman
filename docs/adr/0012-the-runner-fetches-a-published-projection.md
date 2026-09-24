# 0012 — The runner fetches a published projection; it never reads the rule stack

- **Date:** 2026-09-23
- **Status:** accepted

## Context

**The runner executes on a PUBLIC repository's Actions (`adr/0002`) and must build its
prompts from the rule stack (FR-19), which lives in the owner's personal config repo —
a PRIVATE one.** Nothing recorded how the one reaches the other. Found on 2026-09-23
while cutting Phase 2, whose tracer bullet cannot run without it.

## Decision

**The rule stack's own repository publishes the projection; the runner only downloads
it.**

1. **Publish on push.** On every push to `main`, a workflow in the personal config repo
   checks out `alvintoh/forge-wingman` (public), runs the projection generator against
   itself, uploads both projections to Cloud Storage under `projections/<sha>/`, then
   moves a `projections/current` pointer to that sha.
2. **Fetch with the credential the runner already has.** The runner exchanges its
   GitHub OIDC token for a short-lived GCP credential — the Workload Identity
   Federation binding it needs for Firestore anyway — reads `current`, fetches that
   sha's projection, and records the sha on the run (FR-6).
3. **Fail closed.** A missing pointer or projection stops the run. There is never a
   fallback to an older projection.
4. **No path filter, no expiry.** A path filter would be a second copy of the
   generator's input list, and drift there is silent staleness. An age rule would
   eventually delete the current projection of a stack that sat unchanged, and every
   run would then refuse. About 0.3 MB a push.

For the tracer bullet, step 1 may be done by hand: run the generator locally, upload.

*Established parts: OIDC over stored secrets (GitHub's Actions security hardening),
least privilege, content addressing, fail-safe defaults. Assembling them this way is a
judgment call.*

## Consequences

- **No credential to the private repo exists anywhere.** The source pushes its
  artifact out and nothing reaches in, which also settles where a later dispatcher
  would have had to hold one.
- **Speed is a wash; the credential is the gain.** One file is one request from GitHub
  or from Cloud Storage. A finished file in a private GitHub repo would still need a
  token on the public runner; Cloud Storage needs only the keyless one. The per-run
  saving comes from building once per sha rather than once per run.
- **The generator moves into `forge-wingman`**, since FR-19 is product logic and the
  publishing workflow needs it from a public checkout.
- **The employer layer never reaches the bucket** — the generator drops it from both
  projections.
- **FR-19's cacheable prefix is stable by construction** — every run of one sha reads
  the same bytes.
