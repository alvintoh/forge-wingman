# infra

OpenTofu for the project's stores and the runner's keyless credential. The state
bucket `gs://forge-wingman-tfstate` is bootstrapped by hand and is not managed here.

```sh
gcloud auth application-default login   # OpenTofu reads ADC, not the gcloud login
tofu -chdir=infra init
tofu -chdir=infra plan
tofu -chdir=infra apply
```
