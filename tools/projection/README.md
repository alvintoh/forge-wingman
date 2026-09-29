# projection

Generates FR-19's two agent prompts from the owner's layered rule stack, which
lives in a private config repository that is never named here: a full projection
for the plan agent, and a trimmed one for the unattended build agent that drops
every human-in-the-loop section and appends a short protocol for resolving what
the rules leave open. Every claim about the output is asserted against the
emitted text, so a stack edit that breaks one fails the run rather than shipping
a silently different prompt.

    python3 tools/projection/gen_projection.py <rule-stack-root> --out <dir>

`<rule-stack-root>` may instead come from `RULE_STACK_ROOT`. `--out` is required:
the projections are build output, and no repository should hold them.

The tests run the generator against a synthetic stack:

    python3 -m unittest discover -s tools/projection

## Publish

Every push to the rule stack's `main` publishes: a workflow in that repository
checks out this one at a pinned commit, runs the generator before it holds any credential,
creates both files under `projections/<sha>/`, and moves `projections/current` to
that sha only if the sha it names now is an ancestor. It runs as the
`wingman-publisher` identity, which may create projections and overwrite
`current` but never delete or replace a published sha; see `infra/README.md`.
A generator change here reaches the publish only when that pin is bumped.

## Hand publish

The fallback when the workflow cannot run. It generates from a clean
`git archive` of the rule stack's HEAD, never the live tree, so the published sha
is guaranteed to contain exactly what was projected, and it must only move
`current` forward.

```sh
SHA=$(git -C <rule-stack> rev-parse HEAD); SRC=$(mktemp -d); OUT=$(mktemp -d)
git -C <rule-stack> archive "$SHA" | tar -x -C "$SRC"
python3 tools/projection/gen_projection.py "$SRC" --out "$OUT"
gcloud storage cp "$OUT"/projection-{plan,build}.md "gs://forge-wingman-projections/projections/$SHA/"
printf %s "$SHA" | gcloud storage cp - gs://forge-wingman-projections/projections/current
```

The workflow warns and leaves `current` alone when the sha it names is not an
ancestor of the pushed one, as after a force-push to the rule stack's `main`.
Recover by overwriting it by hand against the generation just read, so a
concurrent publish cannot be lost:

```sh
GEN=$(gcloud storage objects describe gs://forge-wingman-projections/projections/current --format='value(generation)')
printf %s "$SHA" | gcloud storage cp - gs://forge-wingman-projections/projections/current --if-generation-match="$GEN"
```
