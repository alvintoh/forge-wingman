# Forge Wingman

An unattended delivery tool: it picks up Linear tickets delegated to it, plans and
builds each one in an isolated run, and leaves a pull request for review.

**The design lives in `docs/tech-design-v1.md` and `docs/adr/`.** They are copied here from the
forge-vault vault, which is where they are edited — never edit them in this repo.

## Use

Hand a ticket over by **delegating it to the Forge Wingman app in Linear**. An app
cannot be an assignee, so delegation is the trigger and the human assignee is left
intact.

The runner reads only the issue's **title and description**, never its comments.
A plan agreed in a comment has to be copied into the description before
delegating, or the run plans without it.

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

A ticket that changes a file under `.github/workflows/` is built and pushed by
hand. The run's GitHub App has no permission to push workflow changes, so a plan
naming one stops before the build starts, and a build that edits one anyway stops
before the push as `stopped/workflow-change`, naming the files; its commit is kept
in the run's bundle artifact.

The dispatcher looks for new tickets as soon as one is delegated: Linear's webhook
reaches `cmd/webhook`, which starts a poll at once; Cloud Run takes about 2 minutes to
start the job's container, and the poll itself takes a few seconds. Cloud Scheduler also wakes
the job **every 15 minutes**, so a delivery that never arrives costs one interval, not
the ticket. One execution is one poll.

A run is not started while its model's peak-price window is open or would be
reached before the run finishes, so a run delegated in those hours waits out the
window: the dispatcher holds it, the run's row shows `waiting_on: peak-hours`,
and the next poll starts it once the window has passed, unattended. The windows
are the model's `peak` in `internal/providers/providers.json`, so a model whose
rate carries none is never held.

Two modes decide it: `off-peak-only`, the default above, and `always`, which
starts a run at any hour. `runner peak-mode` prints the mode and
`runner peak-mode -mode always` sets it; `-mode off-peak-only` restores the
default. The surface's Peak hours view shows which models price peak hours and
when, whether a run is being held now, and the same switch, so the mode need not
be read from a terminal. The next poll reads it, so a switch needs no redeploy —
see [Operate](#operate) for the command and the environment it needs.

## Operate

These subcommands run from a local checkout against the store itself, for the
actions a run's workflow must not take on its own. Each needs the same three
variables:

```sh
export GOOGLE_CLOUD_PROJECT=<project>   # the store's project
export WINGMAN_ACCOUNT=<owner>          # the account every run acts for
export GITHUB_REPOSITORY_OWNER=<owner>  # the account the repository belongs to
```

| Command | What it does |
|---|---|
| `runner enable-provider -provider <name>` | admits a halted provider back to dispatch |
| `runner reset-breaker` | clears the breaker a systemic stop tripped, admitting dispatch again |
| `runner peak-mode [-mode off-peak-only\|always]` | prints the peak mode, or sets whether a run waits out its model's peak-price hours |
| `runner autonomy [-level 0..5]` | prints the autonomy level, the trailing rewrite rate and each level's advance condition with the numbers behind it, recomputes and stores the earned-level snapshot, or sets the level for the next claim |
| `runner cd -repo <owner/name> [-deploying]` | prints whether the repository is recorded as deploying on merge, or records the flag the level-4 condition requires |
| `runner requeue -run-id <id>` | returns a stopped or abandoned run to the queue for another attempt |
| `runner backfill-queued-at [-dry-run]` | stamps `queued_at` on the queued and planned runs written before the field existed |
| `runner plan-define -provider <name> …` | records a provider plan's price, billing, limit behaviour, pages and harnesses |
| `runner plan-optin -provider <name> [-private]` | records the owner's consent to a per-token provider's spend, or with `-private` to private repositories' runs on its free tier |
| `runner plan-verdict -provider <name> …` | records the verdict on a plan's terms with its wording and source |
| `runner plan-reply -provider <name> …` | records a vendor reply and the verdict it leads to |
| `runner plan-list` | prints every plan side by side, flagging the ones to look at |

Read-only: `plan-list` only reads, and `peak-mode` without `-mode`, `cd` without `-deploying` and `backfill-queued-at -dry-run` write nothing, so
they are safe to run against the store to look. Every other command above writes to it, `autonomy` included: it recomputes and
stores the earned-level snapshot even without `-level`.
All refuse an account that is not the repository's owner, which guards against the wrong
account by mistake only: Firestore IAM decides who may write.

## Shape

One repository, one product, two projects — the Go module and `web/` — and four
deployables sharing one store. The projects share no packages across the language
line, so there is no workspace and no `apps/` level:

| Path | Deployable | Trigger |
|---|---|---|
| `cmd/dispatcher` | Cloud Run job | the webhook, and Cloud Scheduler every 15 min |
| `cmd/webhook` | public Cloud Run service; Linear's signature is the gate | Linear's agent-session webhook |
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

The store tests and the runner's record tests run against a Firestore emulator and
skip unless `FIRESTORE_EMULATOR_HOST` is set, so a green `make check` on a fresh
clone runs the gate but not those tests. Start the emulator the way CI does, then
run the gate against it:

```sh
docker run -d --rm -p 8080:8080 \
  gcr.io/google.com/cloudsdktool/google-cloud-cli:586.0.0-emulators@sha256:7617d937e9360d769de4ef66266a8caae503c1dbbd050c93404d3f30045c5125 \
  gcloud emulators firestore start --host-port=0.0.0.0:8080
until curl -fs http://localhost:8080 >/dev/null; do sleep 2; done
FIRESTORE_EMULATOR_HOST=localhost:8080 make check
```

The loop waits for the emulator to answer, as CI does before its tests. The frontend has
its own check:

```sh
cd web && bun run lint && bun run fmt:check && bun run build
```

A run's own pre-PR check gate runs those Go gates in the checks image and, when the
run's diff touches `web/`, the frontend check after them, so a run's verdict fails
wherever CI's `web` job would. The checks image is the image `go.mod`'s toolchain
pins, and the workflow naming it is not one a run's App may change, so it carries no
bun: the model job's bun — installed for its harnesses — is mounted into the web steps'
containers, and the frozen-lockfile install resolves from the bun cache the runner
fills beforehand, those containers having no network.

Once those gates pass, the pre-PR loop probes the guards the diff adds before it
reviews anything: it replaces each added guard's condition with `false` and runs that
package's tests in the same image, so a guard no test covers becomes a finding the fix
round has to answer. At most five guards are probed per scan, one at a time, and each
file is restored before the next.

Go's `go.mod` sets **`go 1.27`** (anything from 1.22 has the stdlib `ServeMux` routing the
services use) and the **toolchain** that builds it; raise the toolchain deliberately.
