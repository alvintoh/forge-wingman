# 0005 — No orchestration framework, and no Python

- **Date:** 2026-09-21
- **Status:** accepted

## Context

`engineering-defaults` is emphatic here: *"Build the proper stack from the start.
Vertex AI + Cloud Run + LangGraph Server"*, *"Python — only for LangGraph +
FastAPI"*, and *"LangGraph runs as its own process with FastAPI as the gateway."*
Declaring this delta explicitly rather than skipping it silently, because a default
quietly omitted is the worst of the three failure modes.

`engineering-defaults` also supplies the deciding test, quoting Anthropic:
**workflows** are *"LLMs and tools orchestrated through predefined code paths"*;
**agents** *"dynamically direct their own processes and tool usage"*.

Forge Wingman's agent is unambiguously the second — it reads a ticket, decides what
to change, and edits files it chose. **And that agent is opencode's, not ours.**
The PRD settled opencode as the harness, with per-agent `model`, `prompt` and
`permission`; FR-14 makes model routing configuration; FR-3 and FR-4 are phases *of
an opencode run*.

So the orchestration layer already exists as a third-party program we invoke. A
graph engine on top would be orchestrating an orchestrator.

> ⚠️ **This reaches Forge Octant's conclusion from the opposite premise, and the
> difference must not be flattened.** That product's `adr/0003` argues *the model
> never picks the path — the collector does, in Go, and the model fills two nodes.*
> Here the model **does** pick the path. Same decision, different reason: there the
> workload is below the agent rung; here it is at the agent rung and the rung is
> already implemented by someone else.

*(⚠️ The NFR-2 clause below is VOID from 2026-09-21 — NFR-2 now selects on
performance alone. Vertex AI is still displaced, by the constraints table
settling OpenCode Go, so this record's decision is unaffected; only the
supporting reason is.)*

Vertex AI is displaced by the PRD rather than by this record: NFR-2 requires a
zero-retention provider that does not train on submitted content, and the
constraints table settled OpenCode Go with pay-per-token as the documented fallback.

## Decision

**No orchestration framework, no Python, and no separate tracing vendor.** The
dispatcher is a state machine in Go over Firestore; the agent is `opencode run`
invoked as a subprocess by the runner.

## Consequences

- **No second runtime and no process boundary.** A language boundary is a process
  boundary, and there is no workload here to pay for one — `engineering-defaults`'
  own Python rule removes itself once LangGraph is gone.
- **What LangGraph would have provided, and where it already lives.** Its value is
  a state machine with checkpointing and human-in-the-loop. The state machine is
  the dispatcher; the checkpoint is FR-6's run record; the human-in-the-loop is
  FR-5's draft PR and FR-24's retry-with-a-note. The product needs what LangGraph
  does, and already has all three.
- **FR-15's sizer is one structured-output call**, not a graph. *Python is required
  when you RUN models, not when you CALL them* — and NFR-6 forbids local inference
  outright, so nothing reaches torch, transformers or a dataframe.
- **Evaluation is the run record, and that is a strength rather than a gap.** FR-6
  persists model per phase, tokens in/out/cached, cost, duration and outcome, which
  is what FR-21's admission gate reads. A public benchmark cannot answer *"does this
  model honour my whole rule set on my repositories"*, which is the only question
  that decides mergeability here.

  ⚠️ *Amended 2026-09-22, twice. This cited FR-21 promoting a challenger on rewrite
  rate — superseded, because rewrite rate cannot see an FR-9 violation; the FR-6
  dependency survives and is stronger, since admission needs per-run evidence. It
  also said ~53,500 tokens, which was the UNTRIMMED stack; the build projection
  measures ~40,000. The decision this ADR records is unaffected either way.*
- **Langfuse stays available and is not adopted.** It would be a second copy of data
  the product must hold anyway. It is OTEL-native, so Go can export to it later
  without a rewrite if tracing is ever wanted beyond the record.
- **The risk accepted:** new agent tooling ships Python-first, so a non-Python stack
  pays continuously rather than once. Mitigated by the fact that the agent itself is
  a third-party CLI — the frontier arrives through opencode's releases, not through
  our imports.
