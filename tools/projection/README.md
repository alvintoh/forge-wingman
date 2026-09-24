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

## Hand publish

Phase 3 automates this. Until then it is run by hand, and it generates from a
clean `git archive` of the rule stack's HEAD, never the live tree, so the
published sha is guaranteed to contain exactly what was projected.

```sh
SHA=$(git -C <rule-stack> rev-parse HEAD); SRC=$(mktemp -d); OUT=$(mktemp -d)
git -C <rule-stack> archive HEAD | tar -x -C "$SRC"
python3 tools/projection/gen_projection.py "$SRC" --out "$OUT"
gcloud storage cp "$OUT"/projection-{plan,build}.md "gs://forge-wingman-projections/projections/$SHA/"
printf %s "$SHA" | gcloud storage cp - gs://forge-wingman-projections/projections/current
```
