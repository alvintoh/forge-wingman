# 0015 — Command Code is the agent harness, on its GOAT plan

- **Date:** 2026-10-05
- **Status:** accepted — the harness and its defaults are built (FRG-41, FRG-44); removing opencode and swapping the windows is FRG-43, in progress; metering the windows is FRG-47, not built; partly superseded by `adr/0016` (the harness half: omp is the default, opencode the fallback, and the Command Code CLI interactive-only; the GOAT provider decision stands)
- **Supersedes:** the harness and provider named in `adr/0005`'s Decision and `adr/0010`'s build seam and *Starting configuration*; `tech-design-v1` §AI *The BUILD seam* and its prompt-caching mechanism; the PRD's OpenCode Go figures under NFR-1 and its *Harness* and *Provider* constraints

## Context

The runner invoked `opencode run` against OpenCode Go: $10/month, with windows of $12 per 5 hours, $30 per week and $60 per month. The harness was always a third-party CLI (`adr/0005`) and the model is FR-14 configuration, so a second harness is an adapter: since FRG-41 the runner picks the harness per attempt from the model id's provider prefix.

The switch is for cost and caching. At the same $10/month, GOAT's windows are larger ($14, $35 and $70 against $12, $30 and $60), and Command Code measured a 95.7% prompt-cache hit over 514 requests and 222M input tokens on DeepSeek V4.1 Flash (2026-10-05), so more of each window buys new work rather than re-sent context.

## Decision

**The Command Code CLI (`cmd`) is the build seam's harness, and its GOAT plan is the provider.**

- **Invocation.** The prompt goes on stdin, never as an argument (the projections exceed Linux's 131,072-byte argument limit). One HOME per run; `--resume <sessionId>` continues the build session across check-fix and review-fix rounds. `--plan` gives the restricted plan and review profile, `--yolo` the build.
- **Credential.** `COMMANDCODE_API_KEY` is written to `~/.commandcode/auth.json` inside the per-run HOME and deleted after each attempt. Without the key every model slot is refused before any agent runs (`model-invalid`). FRG-43 retires the `COMMAND_CODE_OPT_IN` variable FRG-41 added: with one harness it could only switch the product off.
- **Defaults**, overridable per ticket under FR-14:

  | Phase | Model |
  |---|---|
  | plan, build | `command-code/deepseek/deepseek-v4.1-flash` |
  | build fallback | `command-code/inclusionai/ling-3.1-flash:free` |
  | review | `command-code/meta/muse-spark-1.3-contributor` |

  An exhausted allowance advances to the next model in the order (FR-26). Review has one entry, so an exhausted allowance there stops that phase.
- **Windows.** GOAT is $10/month with windows of **$14 per 5 hours, $35 per 7 days and $70 per month** (verified 2026-10-05: https://commandcode.ai/docs/plans/goat). They replace OpenCode Go's as FR-22's configuration. *Updated (2026-10-06): the plan also sets a monthly cap per model, on top of the $70 plan-wide window; caps differ by model, and DeepSeek V4.1 Flash, the default plan and build model, gets **$60** (read from the plan's 'What's included'). Both bind: the $70 window caps all models together, and Flash also stops at $60, so metering only the $70 admits Flash runs the plan then refuses, and the build falls back to the free model. Whether the per-model cap also bounds the 5-hour and weekly windows is unconfirmed. *Updated (2026-10-07): metering the per-model cap is FRG-62; FRG-56 moves the plan-wide windows into configuration and keeps $70.* NFR-1's USD 30/month cash ceiling is unchanged, and $10 sits inside its USD 10–20 band.
- **opencode goes.** FRG-43 deletes its adapter and the Zen model probe.

## Consequences

- **The windows meter nothing yet.** A Command Code run records a provider cost of 0, so FR-22 admits against windows that never fill and NFR-1's per-run cost cap reads zero. FRG-47 fills the cost from the CLI transcript's `costUsd`. Until then the windows are configuration, not enforcement, and the 60-minute duration cap is the only per-run bound that bites.
- **The review tier is public repositories only.** `muse-spark-1.3-contributor` is a contributor tier, so a private target needs a different review model in its configuration. Muse Spark's training-rights posture is recorded per FR-14, not enforced (NFR-2).
- **Prompt caching changes mechanism.** `tech-design-v1`'s `x-opencode-session` header was opencode's; the per-run HOME and `--resume` now keep a phase's prefix warm within a run. Whether a retry (FR-24) reuses the original run's cache is unmeasured.
- **The terms reading does not carry over.** The PRD's unattended-use reading was of OpenCode Go's terms. Command Code's verdict belongs in its `provider_plans` record (`adr/0014`) and is unconfirmed.
- **`models.json` is hand-written and unprobed.** The Zen probe was its only writer and cannot produce a Command Code set.
- `adr/0005` holds: the harness is still a third-party CLI run as a subprocess. The decision seam (`adr/0010`) is unaffected.

## Alternatives

Keeping opencode beside Command Code, which FRG-41's per-provider router allows: FRG-43 removes it instead, with the Zen probe that existed only to serve it.
