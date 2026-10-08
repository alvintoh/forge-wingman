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

`WINGMAN_ACCOUNT` is set by hand, not from an output: it must name the repository's
owner. When it does not, run.yml's ticket, model and pr-meta jobs refuse to run and
the record job records identity-mismatch.

The `COMMANDCODE_API_KEY` repository secret is the Command Code key every model slot runs on.

The rule stack's publish workflow needs two variables on that repository:

| Variable | Output |
|---|---|
| `GCP_WIF_PROVIDER` | `rule_stack_workload_identity_provider` |
| `GCP_PUBLISHER_SERVICE_ACCOUNT` | `publisher_service_account` |

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
each withhold a run until a later poll. A dispatch GitHub refuses releases its
claim, so the next poll takes it again.

A systemic stop — a missing key, an identity mismatch, an exhausted allowance —
trips the breaker, and every poll admits nothing until `runner reset-breaker`
clears it. Each poll also posts the waiting systemic-failure notices to Slack
(`notice.tf` grants the read), class and link only.

Two identities, and no grant in common: the job reads the store, and the
schedule can only start the job.

| Service account | Grants |
|---|---|
| `dispatcher` | Firestore read/write on the queue, read both tokens and `notice-webhook-url` |
| `dispatcher-schedule` | start one execution of the `forge-wingman-dispatcher` job |

Four variables name what OpenTofu cannot guess, and a fifth is optional:

| Variable | What it is |
|---|---|
| `dispatcher_image` | the image to run. The `Dockerfile` builds it with `CMD=dispatcher`; nothing here pushes it, so push it yourself and name the digest |
| `webhook_image` | the webhook service's image, built with `CMD=webhook`; pushed and named by digest the same way |
| `linear_delegate` | the Linear id of the agent the job acts for; a poll whose token is another agent's admits nothing |
| `linear_repositories` | the allowlist. Empty admits nothing, so a forgotten one refuses every ticket rather than dispatching |
| `dispatcher_settings` | optional. A map of `WINGMAN_PLATFORM_CAP`, `WINGMAN_LARGE_CAP`, `WINGMAN_REVIEW_WIP`, `WINGMAN_STABLE_RUNS`, `WINGMAN_RISE_WITHIN` and `WINGMAN_HALVE_BEYOND` to a value; a key left out keeps its default |

### Secrets

The apply creates `linear-token` and `github-token` with no value in them; the
job reads each secret's latest version as a run starts, so a rotation is the
next run's business and not a redeploy's.

```sh
printf %s "$LINEAR_TOKEN" | gcloud secrets versions add linear-token --data-file=-
printf %s "$GITHUB_TOKEN" | gcloud secrets versions add github-token --data-file=-
```

The GitHub token also needs two permissions on every allowlisted repository.
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
webhook). `linear-token` holds a 30-day client-credentials token minted from
the first two:

```sh
curl -s -X POST https://api.linear.app/oauth/token \
  -H 'Content-Type: application/x-www-form-urlencoded' \
  --data-urlencode grant_type=client_credentials \
  --data-urlencode "client_id=$CLIENT_ID" --data-urlencode "client_secret=$CLIENT_SECRET" \
  --data-urlencode 'scope=read,write,app:assignable' | jq -r .access_token
```

`linear_delegate` is that app's user id, `dbffe977-856f-4b88-a728-3cf2ca54fea6`;
every apply must pass it.

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
| `webhook` | Firestore read/write, read `linear-webhook-secret`, `linear-token`, `linear-client-id` and `linear-client-secret`, start one execution of the dispatcher job |

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
then mint a new client-credentials token into `linear-token`. The workspace's
webhook signs with its own secret, so copy the signing secret again afterwards
and start a new revision.

```sh
printf %s "$SIGNING_SECRET" | gcloud secrets versions add linear-webhook-secret --data-file=-
```

A delivery with no usable `agentSession.id` is refused with 400 and logs
`webhookRejected` with the payload's field names, never its values.

Live check, on a test ticket:

1. Delegate it: a job execution starts within seconds, one `runs/` doc appears,
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
