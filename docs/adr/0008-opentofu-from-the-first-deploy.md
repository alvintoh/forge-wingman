# 0008 — OpenTofu from the first deploy, not "once scale makes drift costly"

- **Date:** 2026-09-21
- **Status:** accepted

## Context

`engineering-defaults` records *"Personal: gcloud CLI + Cloud Run first; IaC once
scale makes manual drift costly."* Taken literally that defers IaC indefinitely for
a solo project, because scale never arrives.

`/tech-design` §3e corrects the reading: the deferral **expires at the first hosted
deploy**, and its third trigger is *"a manual change you would have to reproduce
from memory."* Checking that against this product's own Infra section rather than
against "someday":

| Resource | Reproducible from memory? |
|---|---|
| IAP configuration + the authorised principal | no |
| two Cloud Run resources (a job and a service) with their service accounts | no |
| IAM bindings for Firestore, Cloud Storage and `workflow_dispatch` | no |
| the bucket's lifecycle rule **and its disabled soft delete** | **no, and it enforces NFR-7** |
| Cloud Scheduler job and its OIDC invoker | no |

The lifecycle rule settles it on its own. `adr/0004` makes NFR-7's 90-day privacy
bound a property of bucket *configuration* — deliberately, so it cannot be
forgotten the way a delete job can. Configuration that enforces a requirement and
exists only as remembered console clicks is the worst of both: nothing tests it and
nothing records it.

## Decision

**OpenTofu, covering GCP only, written before the first deploy.** OpenTofu rather
than Terraform per `engineering-defaults` — *"same HCL, no BSL licensing risk."*

## Consequences

- **NFR-7's enforcement becomes reviewable.** The retention rule and the soft-delete
  setting are in a file with a diff, not in a console nobody re-reads.
- **The bootstrap is manual by construction** and IaC does not remove it: the state
  bucket and the credentials that reach it cannot be created by the tool that needs
  them. IaC moves where manual setup stops.
- **Coverage is partial and that is stated rather than implied.** GitHub —
  repository settings, branch protection, Actions secrets — is **not** in OpenTofu.
  FR-16 puts auto-merge conditions in branch protection precisely because the runner
  cannot edit them; putting them in IaC the runner's own workflow could reach would
  weaken that. So `apply` into an empty GCP project does **not** reproduce the whole
  product, and a reader must not assume it does.
- **No portability is claimed.** `google_cloud_run_v2_service` has no AWS
  equivalent; no line survives a cloud move. What IaC buys is *tool* portability —
  HCL, state, the plan/apply loop — which is a weaker benefit than the one usually
  cited, and worth having anyway.
- **Not adopted for practice or a CV.** §3f's cost test applies: this is a day of
  work plus a state backend, so it needs a merit reason, and the merit reason is the
  lifecycle rule above.
