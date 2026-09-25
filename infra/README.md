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

The runner also keeps its older binding, `runner_wif`, which any job of the
repository on `main` satisfies. Removing it is the contract step: a separate
change, made only once `infra-smoke` on `main` has passed on the workflow bindings.

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
