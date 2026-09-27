# infra

OpenTofu for the project's stores and the two keyless identities GitHub Actions
runs under. The state bucket `gs://forge-wingman-tfstate` is bootstrapped by hand
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

## Repository variables

Set from the outputs after `apply`:

| Variable | Output |
|---|---|
| `GCP_WIF_PROVIDER` | `workload_identity_provider` |
| `GCP_SERVICE_ACCOUNT` | `runner_service_account` |
| `GCP_MODEL_SERVICE_ACCOUNT` | `model_service_account` |

`WINGMAN_ACCOUNT` is set by hand, not from an output: it must name the repository's
owner. When it does not, run.yml's ticket, model and pr-meta jobs refuse to run and
the record job records identity-mismatch.

## Dispatcher

`dispatcher.tf` is the poller: a Cloud Run job that Cloud Scheduler wakes every
fifteen minutes. One execution is one poll — admit every ticket Linear has
delegated, then start at most one queued run, the one whose `size:` label is
`L` and whose Linear priority is lowest. A dispatch GitHub refuses releases its
claim, so the next poll takes it again.

Two identities, and no grant in common: the job reads the store, and the
schedule can only start the job.

| Service account | Grants |
|---|---|
| `dispatcher` | Firestore read/write on the queue, read both tokens |
| `dispatcher-schedule` | start one execution of the `forge-wingman-dispatcher` job |

Three variables name what OpenTofu cannot guess:

| Variable | What it is |
|---|---|
| `dispatcher_image` | the image to run. The `Dockerfile` builds it with `CMD=dispatcher`; nothing here pushes it, so push it yourself and name the digest |
| `linear_delegate` | the Linear id of the agent the job acts for; a poll whose token is another agent's admits nothing |
| `linear_repositories` | the allowlist. Empty admits nothing, so a forgotten one refuses every ticket rather than dispatching |

### Secrets

The apply creates `linear-token` and `github-token` with no value in them; the
job reads each secret's latest version as a run starts, so a rotation is the
next run's business and not a redeploy's.

```sh
printf %s "$LINEAR_TOKEN" | gcloud secrets versions add linear-token --data-file=-
printf %s "$GITHUB_TOKEN" | gcloud secrets versions add github-token --data-file=-
```

### A ticket

A delegated issue is queued only with both labels, and is recorded as refused
otherwise, in `dispatch/rejected-<identifier>`:

| Label | Values | Without it |
|---|---|---|
| `size:` | `S`, `M`, `L` | refused `no-size`; `XL` is refused `size-above-ceiling` |
| `repo:` | `owner/name`, allowlisted | refused `no-repository` or `repository-not-allowlisted` |

Sizes are hand-set labels: nothing infers a size from the issue's own estimate.

Rollout: apply with the three variables, add the two secret versions, delegate
one issue to the agent, then read the job's log for the poll that took it.

```sh
gcloud logging read 'resource.type="cloud_run_job"' --freshness=1h --limit=50
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
