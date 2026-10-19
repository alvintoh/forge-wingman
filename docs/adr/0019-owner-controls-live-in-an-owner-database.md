# 0019 — Owner controls live in an owner database the runner cannot write

- **Date:** 2026-10-20
- **Status:** proposed — lands through the stored-allowlist ticket, then the phase-state ticket

## Context

The surface now adds projects (`design/inbox-*-v1.svg`, the Projects card), and the dispatcher admits only tickets whose repository is allowlisted, so the allowlist moves off the `linear_repositories` OpenTofu variable (`infra/variables.tf:56`), which only a deploy can change. Driving a phase adds a second owner control: each project's phase state, running, stopping or stopped, where stopping refuses new claims while the runs in flight finish.

Both are controls over what the runner does, so the runner must not be able to change them. It cannot today be stopped from doing so in the `(default)` database: the runner's identity holds `roles/datastore.user` project-wide (`infra/identity.tf:113`), and Firestore IAM cannot be scoped below a database. ADR 0014's owner-only plan records hold by convention for the same reason.

## Decision

- **A named Firestore database, `owner`, holds the owner's controls:** `projects/<id>` (the Linear project, its repository and when it was added) and each project's phase state (FR-34). No classification is stored: the dispatcher reads each repository's visibility live (`internal/dispatcher/github.go:105`), and NFR-5's declaration was dropped because nothing read it.
- **The runner's binding excludes it by IAM condition:** `resource.name != "projects/forge-wingman/databases/owner"`, the form Firestore documents for "all databases except one" (Create and manage databases, read 2026-10-20). IAM's attribute reference also recommends pinning `resource.type` beside `resource.name`; the type string for a Firestore database is not on that page, so the IaC ticket verifies it before adding one. The dispatcher reads the database; the surface and the owner CLI write it.
- **A project's last activity is read, not stored:** one indexed query per project, the newest `updated_at` among `runs` whose `repo` is the project's repository, limit 1. Measured 2026-10-20: 65 run rows, 62 carrying `repo`, so a page of three projects costs three reads and needs no rollup. The trigger to store a rollup instead is a page view needing more than a few hundred reads.
- **Reachability is reported by the dispatcher, not checked by the surface.** On each poll the dispatcher records, in `(default)`, whether it could read each listed repository with its own token (the reads admission already makes: the repository and its open pull requests) and the reason when it could not. The surface only reads that record, so it holds no GitHub credential for this, and a report grants nothing, which is why it needs no place in `owner`. A project's START stays disabled until its repository reads as reachable.

## Consequences

- IAM refuses a runner write to the allowlist or a phase state, whether from a bug or a prompt-injected agent.
- Only one database per project gets Firestore's free quota, so `owner` is billed per operation: about 150 reads a day (every dispatcher poll and webhook wake reads it, plus surface views), under $0.01 a month, with no fixed cost.
- The Cloud console does not enforce database-level IAM conditions; the gate holds on the API and client-library path, which is the only path the runner has.
- **The runner's GitHub App installation cannot be verified by the system:** the dispatcher holds a token, not the App key (a run-workflow secret), and the surface holds neither. A repository the token reaches but the App does not shows as reachable and first fails as a stopped run, so the add dialog states the install as an owner step beside a link.
- **Reachability is as fresh as the poll** (10 or 15 minutes), so a newly added project reads "checking" until the next one.
- Moving ADR 0014's `provider_plans` into this database is a follow-up, not part of this decision.

## Alternatives

- The `(default)` database, owner-written by convention (rejected: the control would rest on no code ever writing it, which fails fail-safe defaults).
- Keeping the OpenTofu variable (rejected: the surface could not add a project, and every addition would need a deploy).
