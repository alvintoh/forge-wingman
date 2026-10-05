# Forge Wingman

An unattended delivery tool: it picks up Linear tickets delegated to it, plans and
builds each one in an isolated run, and leaves a pull request for review.

**The design lives in `docs/tech-design-v1.md` and `docs/adr/`.** They are copied here from the
forge-vault vault, which is where they are edited — never edit them in this repo.

## Use

Hand a ticket over by **delegating it to the Forge Wingman app in Linear**. An app
cannot be an assignee, so delegation is the trigger and the human assignee is left
intact.

The ticket also needs two labels, and may name the models its phases run
on with three optional ones:

| Label | Value |
|---|---|
| a size label | `size:S`, `size:M` or `size:L`, hand-set — nothing infers a size from the issue's own estimate |
| a repo label | `repo:owner/name`, naming a repository the dispatcher is allowed to run in |
| a model label | `model:provider/model`, the build model — overrides the default in `internal/runner/models.json` |
| a review model label | `review-model:provider/model`, the pre-PR review model — overrides the `review_models` default in `run.yml` |
| a plan model label | `plan-model:provider/model,…`, the plan phase's models, comma-separated in order (the first is the main model, the rest backups) — overrides the `plan_models` default in `run.yml` |

A model label wins over the run workflow's own `workflow_dispatch` input,
which in turn wins over the default. A review model equal to the build model
is refused — the review is never the builder checking its own work — so a
review model naming the default build model, or a build model naming the
default review model, is refused too.

`size:XL` is a fourth value the dispatcher recognises and refuses. One run is not
measured to carry it, so it is held above the ceiling rather than rejected as
unreadable.

Without both labels the ticket is refused rather than queued, and the reason is
recorded against it in the store as `dispatch/rejected-<identifier>`: `no-size` for
no size label, `size-unknown` for a size that is not `S`, `M` or `L`,
`size-above-ceiling` for an `XL`, `no-repository` for no repo label,
`repository-not-allowlisted` for a repo it may not run in, and `ticket-invalid` for a
ticket that fails its own checks. A model a run may not use is refused the
same way: `model-malformed` for a label that is not `provider/model` (or a
plan list with an empty or repeated entry), `review-model-is-build-model` for
a review model equal to the build model, `model-provider-unconfigured` for
a provider with no plan record with its billing recorded, and
`model-per-token-not-opted-in` for a per-token provider the owner has not
opted in.

The dispatcher looks for new tickets **every 15 minutes**: Cloud Scheduler wakes the
job, and one execution is one poll. A ticket delegated just after a poll waits for
the next one.

## Shape

One repository, one product, two projects — the Go module and `web/` — and three
deployables sharing one store. The projects share no packages across the language
line, so there is no workspace and no `apps/` level:

| Path | Deployable | Trigger |
|---|---|---|
| `cmd/dispatcher` | Cloud Run job | Cloud Scheduler, every 15 min |
| `cmd/runner` | Go binary in a per-repo GitHub Actions workflow | dispatched per run |
| `cmd/surface` | Cloud Run service behind IAP, serving `web/` | HTTP |
| `web/` | Vite + React + TanStack Router/Form SPA, embedded in `surface` | — |

## Deploy

See [`infra/README.md`](infra/README.md) for deployment steps and infrastructure details.

## Develop

```sh
cd web && bun install     # once
make dev                  # backend :8080 + frontend http://localhost:5173, Ctrl-C stops both
make ui                   # the same, in a process-compose UI: a pane per process
```

For the single binary exactly as it ships: `cd web && bun run build && cd .. && go run ./cmd/surface`,
then http://localhost:8080. `make ui` needs `brew install f1bonacc1/tap/process-compose`.

## Checks

```sh
make check
cd web && bun run lint && bun run fmt:check && bun run build
```

Go's `go.mod` carries a **1.22 floor** (stdlib `ServeMux` routing) and the **toolchain**
that builds it; raise the toolchain deliberately.
