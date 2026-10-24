# infra

OpenTofu for the project's stores and the keyless identities GitHub Actions runs
under. The state bucket `gs://forge-wingman-tfstate` is bootstrapped by hand
and is not managed here.

```sh
gcloud config set project forge-wingman  # ADC takes its quota project from this
gcloud auth application-default login   # OpenTofu reads ADC, not the gcloud login
tofu -chdir=infra init
tofu -chdir=infra plan
tofu -chdir=infra apply
```

## Identities

Each service account is bound to the workflow files whose jobs may impersonate
it, matched on the token's `job_workflow_ref` from `main`. Those bindings follow
the repository NAME (`github_repository`) while the provider's condition follows
its numeric id, so a rename or transfer fails closed until the variable is updated.

No binding admits a job by repository alone, so a job in any other workflow of
this repository — the model's included — cannot become the runner.

| Service account | Workflows | Grants |
|---|---|---|
| `runner` | `run.yml`, `infra-smoke.yml` | Firestore read/write, completions create, projections read |
| `wingman-model` | `model.yml`, `model-smoke.yml` | completions create, projections read |
| `wingman-publisher` | any job of the rule stack on `main` | projections create; get and overwrite of `projections/current` only |
| `wingman-pr-review` | `pr-review.yml` | read of `linear-review-client-id` and `linear-review-client-secret` only |

The publisher is the one identity for another repository: the rule stack, which is
private, so nothing here names it. It has its own provider, `rule-stack`, whose
condition pins the rule stack's numeric id (`rule_stack_repository_id`), the owner
and `refs/heads/main`, and its binding matches that id rather than a workflow file.
The `github` provider is unchanged and still admits this repository alone.

## Repository variables

Set from the outputs after `apply`:

| Variable | Output |
|---|---|
| `GCP_WIF_PROVIDER` | `workload_identity_provider` |
| `GCP_SERVICE_ACCOUNT` | `runner_service_account` |
| `GCP_MODEL_SERVICE_ACCOUNT` | `model_service_account` |
| `GCP_PR_REVIEW_SERVICE_ACCOUNT` | `pr_review_service_account` |

`WINGMAN_ACCOUNT` is set by hand, not from an output: it must name the repository's
owner. When it does not, run.yml's ticket, model and pr-meta jobs refuse to run and
the record job records identity-mismatch.

The `COMMANDCODE_API_KEY` repository secret is the Command Code key every model slot runs on.

The `GEMINI_API_KEY` repository secret is the Google Gemini API key the free-tier plan
runs on. It is set by hand, like the other secrets, and a run reaches it only when its
own plan's allowance is spent and the dispatcher moves it to the free tier
(`last_resort_plans` in `internal/providers/providers.json`). It needs no paid Gemini
plan: the free tier is reached with the key alone. Until it is set, a run that falls
through to the free tier stops as credential-absent rather than starting.

A second paid plan is added by hand: its entry in `internal/providers/providers.json`
(base URL, key secret name, windows, rates, harnesses and the `models` a run moved onto it
starts on), its name after the first in `paid_plans`, its key as a repository secret
that `run.yml` passes to the runner (a workflow edit the agent cannot push), and an owner
`runner plan-define` record for it, with `runner plan-optin` as well where it bills per token.
Until the secret is passed a run moved onto the plan stops as credential-absent. Do not enable
a plan before its unattended-use terms are ruled on.

A harness whose CLI is not open-source needs `runner plan-optin -provider <name> -harness <name>`
as well, and even then it runs only for a command that reads the plan record — the owner's own
`runner plan-smoke` or `review-smoke`, run where `GOOGLE_CLOUD_PROJECT` is set. A build or PR-review job holds
no plan record and never reads opt-ins, so it never selects one: it runs the plan's next harness instead, and
`runner plan-list` shows each plan's harnesses with their licences, open-source pairs first.

`WINGMAN_DISPATCHER_RUN_URI` is the dispatcher job's Cloud Run run endpoint, which
run.yml's `record-plan-stage` job calls to wake the dispatcher for the build stage
(`https://<region>-run.googleapis.com/v2/projects/<project>/locations/<region>/jobs/forge-wingman-dispatcher:run`,
the `dispatcher_run_uri` local in `dispatcher.tf`). The plan stage is inert until
the dispatcher sends `stage=plan`, and that wake needs the runner identity to be
able to invoke the job.

`WINGMAN_PR_REVIEW_MODELS` is optional: the models `pr-review.yml` reviews with, in order,
comma-separated provider/model. Unset, it reviews with
`command-code/poolside/laguna-s-2.1-free`; a paid model is an edit of the variable.

`WINGMAN_AGENT_IMAGE` is required by the build and plan stages and optional for the rest:
an agent image pinned by digest that provides omp, git and sh. It runs those
stages' agent CLIs, and those of `pr-review`, `review-smoke` and `plan-smoke`, in a
container of that image, which mounts only the worktree (an empty scratch directory for
those three), the harness home and the git common directory; the build and plan stages run the CLI's version command in that
container, over those mounts, before the first agent round, so an image missing the
binary stops the run with stop reason `agent-container`. Unset, the build and plan stages
refuse to start, and `pr-review`, `review-smoke` and `plan-smoke` run their agents on the
runner.

`WINGMAN_AGENT_EGRESS` is `on` or `off`, where empty is off, and it needs
`WINGMAN_AGENT_IMAGE` set — a stage refuses `on` without an image. It applies to the
build and plan stages only; `pr-review`, `review-smoke` and `plan-smoke` take no egress
setting and stay on the default network. `on` confines that container to the API hosts
of the plans its models name, through a proxy: it reaches those hosts, reaches nothing
else without the proxy, and resolves no public name. `off` leaves it on the default
network. Set it to `off`, or unset it, to undo.

Dispatch the `agent-probe` workflow to prove both. Its `probe` job runs a command the way
an agent is run, from the same job, and it must read no credentials file and write none of
the step's file-command files; its `egress` job proves the container reaches its model
API host through the proxy, and nothing else without it. Both take the image from
`WINGMAN_AGENT_IMAGE` unless the dispatch input names one.

The rule stack's publish workflow needs two variables on that repository:

| Variable | Output |
|---|---|
| `GCP_WIF_PROVIDER` | `rule_stack_workload_identity_provider` |
| `GCP_PUBLISHER_SERVICE_ACCOUNT` | `publisher_service_account` |

## Pull requests and auto-merge

run.yml's pr job opens the PR with the run's GitHub App installation token
(`WINGMAN_APP_CLIENT_ID` variable, `WINGMAN_APP_PRIVATE_KEY` secret), set by hand.
The App needs Contents and Pull requests read/write on this repository and no
Workflows permission, so a run can never push a change under `.github/workflows/`.

`main` is held by the `protect-main` ruleset: the `go` and `web` checks must pass,
and no approval is required. An auto-merge request waits on those checks; the
runner only requests it and can change none of them.

Auto-merge is off until both are set:

| Setting | Value |
|---|---|
| Repository "Allow auto-merge" (`allow_auto_merge`) | on |
| The owner's switch (`runner auto-merge -mode on`) | on, stored at `dispatch/auto-merge`; no document counts as on, one that cannot be read as off |

With the switch on, auto-merge is on when the trailing rewrite rate is 10% or less over
the ten newest measured merged PRs and none of them is an auto-merged PR a later merged
revert names. The switch and the rate are two facts: turning the switch off makes every
PR manual whatever the rate reads. The Autonomy page and `GET /api/autonomy` show the
state, the rate and the reason.

The rate is measured rather than trusted. Each poll reads the run PRs whose merges landed
since the last one and writes onto each run row the lines the agent wrote and the lines
the owner changed after it: a commit authored as the runner (`github-actions[bot]`) counts
as the agent's, every other commit as the owner's.

The `dispatch/autonomy` document and its history, and the `runner autonomy` and
`runner cd` commands, were removed (`adr/0018`); eligibility now follows the rule and the
switch above. Nothing reads those documents or that field — they are left where they are,
so a run row that carries an old `autonomy` field is read exactly as one that does not.

With the switch on and the rate allowing it, a run's S or M PR that has a recorded plan, is not in the review sample,
edits nothing outside that plan, fails no check gate and is not a front-end change
gets the `wingman:auto-merge-eligible` label, which must exist in the repository. A
run with no recorded plan is never labelled, and its PR body says so; an S run is
never planned today, so only an M run can be labelled yet.
`pr-review.yml` reviews every ready PR against its Linear ticket and, only when the review is clean, the PR carries that
label and its head is still the commit it reviewed, requests a squash auto-merge
pinned to that commit, and only on a run's `wingman/` branch. A push to that branch
removes the label and withdraws any request (`automerge-withdraw.yml`). The request is made only while `main` requires `go`
and `web`.

A front-end change is held for the owner, still as a ready PR: a ticket labelled
`area:fe` in Linear, or a PR that changes a file under a path prefix the
`WINGMAN_FRONT_END_PATHS` variable declares for its repository, a JSON object such as
`{"alvintoh/forge-wingman":["web/"]}`. Either one holds it, and a value that is not
valid JSON holds every PR. The PR body names what matched.

The build agent runs in the agent container and the pre-PR checks in the checks image,
so it has no shell on the runner to forge a job output: `build` and `plan-stage` refuse to
start without `WINGMAN_AGENT_IMAGE`, and `build` without its checks image. The review's
verdict comes from its own job, and pr-meta derives the write set from the branch's bundle and measures it against the plan in the run record.
The summary's `ready` flag, which decides whether a PR is a draft when the pre-PR loop
left something open, is still the model's verdict, so the agent's diff can steer it: it is
advisory, not trusted. The gates that cover it are the plan's write set, the check gates,
the required `go` and `web` checks and the pr-review verdict.

The review itself is not proof against the build agent either: the agent writes the
diff the reviewer reads, so it can plant instructions that steer one model to a clean
review. The required checks, the review sample and independent specialist reviews
narrow that; none closes it.

## Pull request review

`pr-review.yml` runs on `pull_request_target` as the file is on `main`, for a
non-draft PR from this repository into `main`, and never checks out or runs the
PR's code. Its `context` job runs as `wingman-pr-review`, mints a read-scoped
token on a second Linear app to read the ticket the PR's title or body names, and
reads the diff through the API; its `review` job holds only the model key; its `post` job comments,
sets the `wingman-review` status on the reviewed commit and makes the merge
request with the App token. A PR naming no ticket gets an `error` status saying why
and no review. The status is not a required check, so a PR is never blocked by it.

The second app exists because one app's tokens must all share one scope: a
client-credentials mint with a different scope revokes every token the app holds,
so a read token minted on the dispatcher's app would revoke the dispatcher's and
the webhook's. To set it up:

1. In Linear, create an OAuth application with client credentials enabled and the
   `read` scope only.
2. Add its client id and secret as the first versions of `linear-review-client-id`
   and `linear-review-client-secret`, by hand, after `tofu apply` creates them.
3. Authorize it once with `actor=app`, so Linear creates its app user and adds it to
   the team: give the app a callback URL (any, such as `http://localhost:8765/callback`),
   open `https://linear.app/oauth/authorize?client_id=<linear-review-client-id>&redirect_uri=<that URL, encoded>&response_type=code&scope=read&actor=app`
   and approve. No code exchange is needed.

If the review's `context` job fails with `the Linear API refused the query: Entity not
found: Issue`, the app's user has lost the team: a workspace admin suspended it (Linear
records `adminSuspended`). Linear refuses to unsuspend an app user ("Users tied to an
OAuth client cannot be directly unsuspended"), so repeat step 3, which reactivates it
and restores its team membership. Revoking its client-credentials tokens does not
suspend it (tested 2026-10-22). Do not suspend `Forge Wingman` or `Forge Wingman Review`
in Linear's Members.

## Rolling out the publisher

1. Merge.
2. `tofu plan`, then `tofu apply`. The plan only adds: the `rule-stack` provider,
   the publisher, its binding and its two bucket grants. Read back the provider's
   condition and the projections bucket's IAM policy.
3. Set the rule stack's two variables above.
4. Pin the publish workflow's checkout of this repository to the merge commit
   from step 1.
5. Merge the rule stack's publish workflow; that push is the first publish.
   `projections/current` must name the merged sha.
6. Dispatch the publish workflow on `main` a second time: it must be green, and
   its upload lines must read 412. If they read 403, the uploads could count 403
   as done for the sha objects, since only this identity's create path produces
   it there.
7. Dispatch the publish workflow from a branch other than `main`: its auth step
   must be refused.

## Dispatcher

`dispatcher.tf` is the poller: a Cloud Run job that Cloud Scheduler wakes every
fifteen minutes. One execution is one poll — admit every ticket Linear has
delegated, then walk the queue in priority order and start every run admission
lets start: the discovered concurrency limit, the platform cap, one run per
repository, the large-run cap, blocking relations and the open-PR review limit
each withhold a run until a later poll. A run the plan's own allowance window or
model cap would withhold starts on the plan's free models instead, a private
repository's only after `runner plan-optin -private`. A dispatch GitHub refuses
releases its claim, so the next poll takes it again. A poll admits only the
tickets the owner has delegated to the agent; it never delegates one itself.

A systemic stop — a missing key, an identity mismatch, an exhausted allowance —
trips the breaker, and every poll admits nothing until `runner reset-breaker`
clears it. Each poll also posts the waiting systemic-failure notices to Slack
(`notice.tf` grants the read), class and link only.

A run whose model prices peak-hour rates waits for the window to pass, unless the
owner switches it off: `runner peak-mode` prints the mode and `runner peak-mode -mode always`
lets runs start at any hour (`-mode off-peak-only` restores the default), read by the
next poll with no redeploy. The surface's Peak hours view shows the same thing and
carries the same switch.

Two identities, and no grant in common: the job reads the store, and the
schedule can only start the job.

| Service account | Grants |
|---|---|
| `dispatcher` | Firestore read/write on the queue, read both tokens and `notice-webhook-url` |
| `dispatcher-schedule` | start one execution of the `forge-wingman-dispatcher` job |

Three variables name what OpenTofu cannot guess, and a fourth is optional:

| Variable | What it is |
|---|---|
| `dispatcher_image` | the image to run. The `Dockerfile` builds it with `CMD=dispatcher`; nothing here pushes it, so push it yourself and name the digest |
| `webhook_image` | the webhook service's image, built with `CMD=webhook`; pushed and named by digest the same way |
| `linear_delegate` | the Linear id of the agent the job acts for; a poll whose token is another agent's admits nothing |
| `dispatcher_settings` | optional. A map of `WINGMAN_PLATFORM_CAP`, `WINGMAN_LARGE_CAP`, `WINGMAN_REVIEW_WIP`, `WINGMAN_STABLE_RUNS`, `WINGMAN_RISE_WITHIN`, `WINGMAN_HALVE_BEYOND`, `WINGMAN_TWO_STAGE` and `WINGMAN_DECISION_TIMEOUT` to a value; a key left out keeps its default. `WINGMAN_DECISION_TIMEOUT` is how long a run waits on an owner decision (e.g. `24h`) before it is stopped as waiting-on-owner. Both the dispatcher job and the surface service read it: the job runs under these settings and the surface's configuration view shows them, so a change needs both applied |

### The queue tie-break

Runs of one priority are claimed oldest-queued first, by a `queued_at` field the
dispatcher stamps whenever it queues or requeues a run. `queue_claim_by_queued_at`
serves that read, and `Candidates` fails every poll until the index is READY, so a
fresh project applies the index before it starts the dispatcher.

A queued or planned row without `queued_at` or without a priority is invisible to that
query. `Candidates` counts the queued and planned rows against those the query can
list and logs `queuedRowsNotListed` with both counts when they differ. To stamp
`queued_at` on the rows lacking it, run `runner backfill-queued-at -dry-run`, then `runner backfill-queued-at`, from a checkout
with `GOOGLE_CLOUD_PROJECT`, `WINGMAN_ACCOUNT` and `GITHUB_REPOSITORY_OWNER` set (the
last two must name the same owner) and credentials that can write the queue. It stamps
each from its `started_at`, else its `updated_at`, skips a row that changed since it was
listed, and reports `stamped` and `skipped` in `queuedAtBackfilled`; running it again
stamps nothing.

A priority edit in Linear reaches the queue only when the app's webhook also delivers
Issue updates.

### Secrets

The apply creates `github-token` with no value in it; the job reads the
secret's latest version as a run starts, so a rotation is the next run's
business and not a redeploy's.

```sh
printf %s "$GITHUB_TOKEN" | gcloud secrets versions add github-token --data-file=-
```

The GitHub token also needs two permissions on every listed repository.
`pull_requests: read`: admission counts the open agent PRs, and a repository it
cannot read leaves that count unknown, which admits only a lone run.
`actions: write`: the dispatch is a `workflow_dispatch`, which GitHub refuses
with 403 "Resource not accessible by personal access token" without it.

`secrets.tf` creates four more, empty and readable by no identity until the code
that reads each lands and grants its own (`notice.tf` grants the dispatcher
`notice-webhook-url`; `linear_app.tf` grants the dispatcher and the webhook the
two client credentials): `linear-client-id` and
`linear-client-secret` (the Forge Wingman Linear app, client credentials on),
`linear-webhook-secret` (Linear issues it once the webhook service has a URL;
the webhook reads it) and `notice-webhook-url` (the Forge Slack incoming
webhook). No Linear access token is stored: the dispatcher mints one per poll
and the webhook one per instance, from the first two, and both revoke what
they mint.

`linear_delegate` is that app's user id, `dbffe977-856f-4b88-a728-3cf2ca54fea6`;
every apply must pass it.

### A ticket

A delegated issue is queued only with both labels, and is recorded as refused
otherwise, in `dispatch/rejected-<identifier>`:

| Label | Values | Without it |
|---|---|---|
| `size:` | `S`, `M`, `L` | refused `no-size`; `XL` is refused `size-above-ceiling` |
| `repo:` | `owner/name`, listed in the owner database | refused `no-repository` or `repository-not-allowlisted` |

Delegating a ticket whose run has already ended does not queue it again; that needs
`runner requeue`.

Sizes are hand-set labels: nothing infers a size from the issue's own estimate.

Rollout: apply with the three variables, add the two secret versions, delegate
one issue to the agent, then read the job's log for the poll that took it.

```sh
gcloud logging read 'resource.type="cloud_run_job"' --freshness=1h --limit=50
```

## Owner database

`owner` is a second Firestore database holding the owner's controls: the repository
allowlist and each project's phase state (`docs/adr/0019-owner-controls-live-in-an-owner-database.md`).
It exists so the runner cannot change what it works on. The runner's
`roles/datastore.user` binding (`identity.tf`) carries a condition that excludes it, and no
other identity's binding changes: the dispatcher, the surface and the webhook keep their
unconditional grants, which already reach every database.

### Apply

Apply it targeted. A plain `tofu apply` also resets the dispatcher's runtime settings
(FRG-150). `dispatcher_image`, `webhook_image` and `linear_delegate` have no default, and
neither target uses them, so a placeholder satisfies the plan and the apply; a target that
did depend on one would need its live value.

```sh
cd infra
# expect: Plan: 2 to add, 0 to change, 1 to destroy.
tofu plan -target=google_firestore_database.owner -target=google_project_iam_member.runner_datastore \
  -var=dispatcher_image=x -var=webhook_image=x -var=linear_delegate=x
tofu apply -target=google_firestore_database.owner -target=google_project_iam_member.runner_datastore \
  -var=dispatcher_image=x -var=webhook_image=x -var=linear_delegate=x
```

The plan reads that way because the runner's binding is replaced create-first, then the
unconditional one is destroyed: IAM grants add up, so destroying first would leave the
runner with no datastore access while a run starts. If an apply stops between the two, both
bindings stay, which is today's access; rerun it.

### List a project

Run from a terminal with `GOOGLE_CLOUD_PROJECT`, `WINGMAN_ACCOUNT` and
`GITHUB_REPOSITORY_OWNER` set (the last two name the same owner): the owner commands check
the account before they write.

```sh
GOOGLE_CLOUD_PROJECT=forge-wingman WINGMAN_ACCOUNT=alvintoh GITHUB_REPOSITORY_OWNER=alvintoh \
  go run ./cmd/runner project-add -linear-project forge-wingman -repo alvintoh/forge-wingman
```

`project-remove` takes `-repo` alone. The dispatcher reads the list once per poll, so an
addition is admitted by the next one.

### Read it back

A policy that reads right is not a policy that refuses, so check with a write and a read.
Each line prints the HTTP status of a document write (`w`) and read (`r`) made as that
identity; the document is removed after. Needs `roles/iam.serviceAccountTokenCreator` on the
three accounts.

```sh
P=forge-wingman
probe() { # $1 service account, $2 database id
  local tok url
  tok=$(gcloud auth print-access-token --impersonate-service-account="$1")
  url="https://firestore.googleapis.com/v1/projects/${P}/databases/${2}/documents/probe/check"
  printf 'w=%s r=%s' \
    "$(curl -s -o /dev/null -w '%{http_code}' -X PATCH -H "Authorization: Bearer $tok" \
      -H 'Content-Type: application/json' -d '{"fields":{"probe":{"booleanValue":true}}}' \
      "${url}?updateMask.fieldPaths=probe")" \
    "$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $tok" "${url}")"
}
for out in runner_service_account dispatcher_service_account surface_service_account; do
  sa=$(tofu output -raw "$out")
  echo "$out owner: $(probe "$sa" owner)  default: $(probe "$sa" '(default)')"
done
```

Expect `w=403 r=403` for the runner on `owner` and `w=200 r=200` everywhere else. Then delete
the probe documents in both databases (`probe/check`) from the console or with the same token.
Also confirm the database exists: `gcloud firestore databases list --project=forge-wingman`.

If the runner gets `200` on `owner`, the condition does not bind: store nothing in `owner`
(no allowlist, no phase state) until the expression, and whether `resource.type` must be
pinned, is settled.

IAM's attribute reference advises pinning `resource.type` beside `resource.name`. Its
Firestore value is not on that page, so the condition uses the name alone; the check
above is what shows it refuses.

## Prompt caching

Every phase of a run sends the same projection prefix to the model, so most of
what it reads is served from the provider's cache. Measured on three runs
(2026-10-14; their run ids were not recorded): 96-98% of a run's input
tokens are cache reads, but a phase's *first* request varies with how warm that prefix still is:

| Run | First request |
|---|---|
| build, started soon after another run on the same sha | 99% cached |
| build, overlapping another run | 64% cached |
| plan, started hours after the last run on that sha | 4% cached, 52k tokens fresh |

A review pass (FR-28) starts a fresh session, so its first request is always
cold: it reads the projection's rules and the diff, and the rules prefix is the
one the build request reads. The 10-16k tokens recorded here for a review's
first request were measured before the pass carried those rules. A phase's
first-request fresh and cached tokens and the cached share of its whole input are
written to the run record (`cache` in `runs/<identifier>`, from the build's
`Summary.Cache`), and the statistics endpoint prints a run's own share per run
(`runs` in `/api/comparison`).

**Why the plan stage runs cold: the gap between runs.** A cache stays warm only
while something reads it, and a plan stage is the phase that most often starts a
long time after the last thing that read the prefix — hours before its own build
in the two-stage dispatch, or hours after the last run of that sha — so its first
request finds the prefix gone. The record reports how cold a run was, not why. (A
run planned by the separate plan-stage dispatch carries no plan entry there: that
job reports no summary at all, its tokens included.)

**Retention is not documented by the provider** (checked 2026-10-14). Command
Code's docs (`https://api.commandcode.ai/docs`, all 60 pages) carry cache prices
and a ~98% hit-rate claim, and one TTL — 15 minutes, on the CLI's own fetch-tool
response cache — but no prompt-cache window. DeepSeek's own cache page is not
reachable from the runner's egress allowlist (`api.commandcode.ai` and
`generativelanguage.googleapis.com` alone), and the repository's own measurement
(2026-10-05: whole-context misses 0.2-1.8 min apart, so not expiry) bounds
nothing hours out.

**Batch delegations that share a prefix.** Delegate the tickets of one sha
together rather than one every few hours: each run's first request then reads the
cache the run before it left warm, and only the batch's first run pays the cold
prefix — the 52k fresh tokens in the table above. This is measured on runs of one
sha: two shas are two prefixes, and nothing here says they warm each other.

## Surface

`surface.tf` holds the identity the surface runs under — the Cloud Run service
that serves the SPA and the JSON views. Its views read the store, and it writes
the two settings an owner switches from a page: `dispatch/auto-merge` for the
auto-merge switch and `dispatch/peak-mode` for the peak mode, which
`cmd/surface`'s own handlers write and nothing else.

**A Firestore grant is made at the project, and no role narrows one to a
document**, so that one-document scope is held by the code rather than by the
grant.

| Service account | Grants |
|---|---|
| `surface` | Firestore read/write; `secretAccessor` on `surface-merge-token` |

### The service

`forge-wingman-surface` is a Cloud Run service behind Identity-Aware Proxy (IAP), turned on
at the service itself, so no load balancer stands in front. IAP signs the caller in with
Google before a request reaches the container; the container reads `PORT`,
`GOOGLE_CLOUD_PROJECT` and, for the merge route, `WINGMAN_MERGE_TOKEN` (below). Two grants make that work: the IAP service agent
(`service-<project number>@gcp-sa-iap.iam.gserviceaccount.com`) holds `roles/run.invoker`
on the service, and `surface_owner_email` holds `roles/iap.httpsResourceAccessor` on it.
Anyone else is refused at the proxy. `invoker_iam_disabled` stays unset.

The provider has no resource for the IAP service agent, and the grant above fails if the
agent does not exist yet. If the apply reports it missing, create it once with
`gcloud services identity create --service=iap.googleapis.com --project=forge-wingman`
and rerun the apply.

### The merge token

`surface-merge-token` holds the GitHub token the merge route acts with. The apply creates
it empty, grants `secretAccessor` on it to the `surface` service account alone, and passes its
latest version to the service as `WINGMAN_MERGE_TOKEN`. Without a value the route answers 503.

Create a fine-grained personal access token as the owner, limited to `alvintoh/forge-wingman`,
with these repository permissions and no others (Metadata read is added automatically). Read
against GitHub's fine-grained permissions page, 2026-10-22:

| Permission | Level | Used by |
|---|---|---|
| Contents | Read and write | `PUT /pulls/{n}/merge` |
| Pull requests | Read-only | `GET /pulls/{n}` |
| Commit statuses | Read-only | `GET /commits/{sha}/status` |

Fine-grained tokens have no Checks permission. The check-runs read needs none while the
repository is public; if it goes private, read the checks through the GraphQL
`statusCheckRollup` that `PullSummaries` already uses.

Cloud Run resolves `latest` when a revision starts, so the value must exist before the service
is applied: secret first, value second, service third:

```sh
cd infra
T=(-target=google_secret_manager_secret.surface_merge_token
   -target=google_secret_manager_secret_iam_member.surface_merge_token)
V=(-var=dispatcher_image=x -var=webhook_image=x -var=linear_delegate=x
   -var=surface_image=<the image digest now serving> -var=surface_owner_email=<owner google account>)
tofu apply "${T[@]}" "${V[@]}"
pbpaste | gcloud secrets versions add surface-merge-token --project=forge-wingman --data-file=-
pbcopy < /dev/null
tofu apply -target=google_cloud_run_v2_service.surface "${V[@]}"
```

Read it back: `gcloud secrets get-iam-policy surface-merge-token --project=forge-wingman` lists the
`surface` service account and nothing else, and `gcloud run services describe forge-wingman-surface`
shows `WINGMAN_MERGE_TOKEN` sourced from the secret. A new revision reads the value again, so
rotate by adding a version and applying the service once more.

### OAuth client and consent screen (once, by the owner)

The project has no organization, so IAP cannot use Google's managed client: it needs a custom
OAuth client and an External consent screen. Neither has a resource in this configuration, and
OAuth clients cannot be created programmatically. Steps from Google's guide to enabling IAP for
external applications (docs.cloud.google.com/iap/docs/custom-oauth-configuration, read
2026-10-20):

1. Console, Google Auth Platform, Branding: Get started. Enter an app name and a user support
   email, set Audience to External, enter a contact email, and Create. If the Audience page reads
   Testing, add `surface_owner_email` as a test user.
2. Console, APIs & Services, Credentials: Create credentials, OAuth client ID, application type
   Web application, with a name. Note the client ID.
3. In that client's Authorized redirect URIs, add
   `https://iap.googleapis.com/v1/oauth/clientIds/<CLIENT_ID>:handleRedirect` and Save.
4. Once the service exists, give IAP the client: a file with
   `accessSettings: {oauthSettings: {clientId: <id>, clientSecret: <secret>}}`, then
   `gcloud iap settings set <file> --project=forge-wingman --resource-type=cloud-run --region=us-central1 --service=forge-wingman-surface`.
   Delete the file afterwards: it holds the client secret.

### Apply

Apply it targeted, for the same reason as the owner database: a plain `tofu apply` also
resets the dispatcher's runtime settings. `dispatcher_image`, `webhook_image` and
`linear_delegate` are not used by these targets, so a placeholder satisfies them;
`surface_image` and `surface_owner_email` are used and need their live values.

```sh
cd infra
T=(-target=google_project_service.this -target=google_cloud_run_v2_service.surface
   -target=google_cloud_run_v2_service_iam_member.surface_iap_invoker
   -target=google_iap_web_cloud_run_service_iam_member.surface_owner)
V=(-var=dispatcher_image=x -var=webhook_image=x -var=linear_delegate=x
   -var=surface_image=us-central1-docker.pkg.dev/forge-wingman/forge-wingman/surface@sha256:<digest>
   -var=surface_owner_email=<owner google account>)
tofu plan "${T[@]}" "${V[@]}"
tofu apply "${T[@]}" "${V[@]}"
```

Expect `Plan: 6 to add, 0 to change, 0 to destroy.`: the IAP API, the `surface` service
account and its datastore grant (declared earlier, not yet applied), the service, and the two
IAP grants.

### Build the image

```sh
docker buildx build --platform linux/amd64 --provenance=false \
  --build-arg CMD=surface --build-arg COMMIT_SHA=<sha> \
  -t us-central1-docker.pkg.dev/forge-wingman/forge-wingman/surface:<sha> --push .
docker buildx imagetools inspect us-central1-docker.pkg.dev/forge-wingman/forge-wingman/surface:<sha>
```

The manifest must list `linux/amd64` alone; Cloud Run rejects an arm64 image. Pass the
digest it prints as `surface_image`.

In zsh, write the tag as `"${R}:${sha}"` when the repository is a variable: `$R:sha` reads `:s...`
as a modifier and pushes to a different repository. Confirm the push reached the registry before
applying: `gcloud artifacts docker tags list us-central1-docker.pkg.dev/forge-wingman/forge-wingman/surface --format='value(tag,version)' | grep <sha>`
must print the digest. `imagetools inspect` on a local build passes even when the push went
elsewhere, and Cloud Run then answers `Image ... not found` and keeps the previous revision
serving.

### Check it

```sh
gcloud run services describe forge-wingman-surface --region=us-central1 --project=forge-wingman
URL=$(tofu output -raw surface_url)
curl -I "$URL"
```

- `describe` shows `Iap Enabled` and a `Ready` condition of True.
- The unauthenticated `curl -I` is refused (a redirect to Google sign-in or a 401/403),
  never a 200.
- The owner's browser, signed in as `surface_owner_email`, reaches `$URL` and the page loads.
- A second targeted plan with the same `T` and `V` arrays reads No changes.

## Webhook

`webhook.tf` is the service Linear delivers agent-session events to. It is
public (`invoker_iam_disabled`), because Linear calls without a Google
identity: the `Linear-Signature` HMAC over the body is the gate, and a delivery
older than 60 seconds is refused. A verified `AgentSessionEvent` with action
`created` writes `dispatch/webhook-<agent session id>`, posts a `thought` on the
session and starts one execution of the dispatcher job; anything else verified
is answered 200 and ignored. A redelivery of a marked session does neither
again, while a new session on the same issue does both. The poll creates
`runs/<identifier>` exactly once, so however many sessions and schedules wake
it, an issue gets one run.

| Service account | Grants |
|---|---|
| `webhook` | Firestore read/write, read `linear-webhook-secret`, `linear-client-id` and `linear-client-secret`, start one execution of the dispatcher job |

`webhook_image` is the image, built with `CMD=webhook`; every apply must pass it,
as it passes `dispatcher_image`. With no signing secret the service still starts
and refuses every delivery with 401, re-reading the secret at most every 30
seconds until it has one, so adding the first value needs no restart. Once it
has a value it keeps it: rotating the signing secret needs a new revision.
The service mints its own Linear token from the client credentials on first
use, mints a successor when Linear refuses it, at most every 30 seconds, and
revokes it on shutdown. A failed wake removes the marker and answers
500, so Linear's retry wakes the job again.

Rollout: push the image, apply, give the Linear app the `webhook_url` output
as its webhook URL with agent session events on, then add the signing secret
Linear issues.

Linear creates a workspace's copy of the app webhook only when that workspace
AUTHORIZES the app, by an authorization-code exchange. Turning webhooks on for an
app the workspace already uses through client-credentials tokens delivers
nothing, and the exchange is refused while such a token exists ("Scope updates
are not supported for client credentials tokens"). So: revoke the client
credentials token (`POST https://api.linear.app/oauth/revoke`), open the
authorize URL with `actor=app`, exchange the returned code and revoke that token,
then let the services mint their own client-credentials tokens again. The workspace's
webhook signs with its own secret, so copy the signing secret again afterwards
and start a new revision.

```sh
printf %s "$SIGNING_SECRET" | gcloud secrets versions add linear-webhook-secret --data-file=-
```

A delivery with no usable `agentSession.id` is refused with 400 and logs
`webhookRejected` with the payload's field names, never its values.

Live check, on a test ticket:

1. Delegate it: a job execution is created within seconds and its poll runs about
   2 minutes later, after the container starts; one `runs/` doc appears,
   and the session shows the `thought`.
2. Force one 500: point the service at a run URI that refuses, delegate a second
   test ticket, then restore the configuration with `tofu apply`. The log must
   show `webhookWakeFailed`, then Linear's retry about a minute later reaching
   `webhookWoke`. A `webhookRejected` with `reason: stale` on the retry means
   Linear resends the original `webhookTimestamp`, and retries can never pass
   the 60-second window.

```sh
gcloud run services update forge-wingman-webhook --region=us-central1 \
  --update-env-vars=DISPATCHER_RUN_URI=https://us-central1-run.googleapis.com/v2/projects/forge-wingman/locations/us-central1/jobs/missing:run
gcloud logging read 'resource.type="cloud_run_revision" AND resource.labels.service_name="forge-wingman-webhook"' --freshness=10m --limit=50
```

## Rolling out the model identity

1. Dispatch `infra-smoke` with `--ref` set to the feature branch and read the
   printed `job_workflow_ref` claims. The GCP steps fail there, since the pool
   trusts `main` only.
2. `tofu plan`, then `tofu apply`. The plan adds resources and changes the
   provider's attribute mapping in place; it destroys nothing.
3. Set `GCP_MODEL_SERVICE_ACCOUNT` from `model_service_account`.
4. Merge.
5. Dispatch `infra-smoke` on `main`: `model` proves the model account's limits,
   and `claims` proves the token carries the `job_workflow_ref` the runner's
   workflow bindings name.
6. Remove `runner_wif` in its own change; `smoke` passing after it proves the
   workflow bindings alone admit the runner.
