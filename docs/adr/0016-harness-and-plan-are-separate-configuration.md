# 0016 — The harness and the coding plan are separate configuration; omp is the default harness

- **Date:** 2026-10-07
- **Status:** proposed — FRG-56 (plan facts in configuration) is salvaged, not merged; the omp adapter is FRG-57, rescoped; the opencode adapter is FRG-65
- *Updated (2026-10-07, later the same day): the default and fallback are a measured ranking, not a choice of tool. omp is the default because it measured best; opencode is the fallback because it measured second ($0.0153 per success against the Command Code CLI's $0.0184). Either is replaced when another harness measures better, which may or may not be opencode.*
- **Supersedes, in part:** `adr/0015`'s harness half (the Command Code CLI as the build seam's harness, its invocation, credential, caching and cost consequences); the 2026-10-05 amendments to `adr/0005`, `adr/0009` and `adr/0010` where they name the Command Code CLI as the harness. **Not superseded:** `adr/0015`'s provider half (the GOAT plan, its windows, the $60 Flash allowance), `adr/0005`'s principle (a third-party CLI run as a subprocess), `adr/0010`'s seam split, `adr/0014`'s plan records.

## Context

One string does four jobs today. The model id's prefix (`command-code/…`) picks the harness, names the billing plan, keys the halt counter and keys the terms verdict. So switching either the CLI or the plan is a code change, which FR-14 says it must not be.

Two measurements made the harness question concrete (2026-10-06, DeepSeek V4.1 Flash billed to GOAT, same task, rules and context, 5 runs each): omp cost $0.0133 per successful task, opencode $0.0153 and the Command Code CLI $0.0184, all 5/5; omp's median wall time was 81.6 s against 139.5 s. GOAT's Provider API (`https://api.commandcode.ai/provider/v1`, OpenAI-compatible) serves any harness on the same key and bills at DeepSeek's list price (audited per request). Neither the harness nor the plan is permanent: another tool or another plan may be better next quarter.

## Decision

**The coding plan and the harness are independent configuration. Changing either is a configuration entry and a secret, never a change to the runner's core.**

- **Plan.** A model id is `<plan>/<model>`; the prefix names who bills. Each plan is one entry in the provider configuration (`internal/providers/providers.json`): its OpenAI-compatible base URL, the name of the secret holding its key, its windows, its per-model allowances, and which harnesses may run on it. The prefix keys the plan record, the halt counter and the verdict, as before; it no longer picks the CLI.
- **Harness.** A harness is an adapter behind the existing `Harness` interface (`internal/runner/harness.go`), one file per harness, named nowhere else (the existing `TestNoHarnessIsNamedOutsideItsAdapter` guard). Which harness runs is configuration: a default, plus an ordered fallback, per plan. **Adding a harness is one adapter file, its recorded fixtures and one configuration line.**
- **Conformance.** Every adapter passes one shared contract suite before it can be selected: headless run from a prompt on stdin, a read-only profile that refuses edit, write and shell (FR-3), session resume across rounds (FR-28), translation to the runner's event shape including token usage, a cost the runner can meter (priced from tokens where the harness's own figure is wrong), bad-key and limit classification, and the credential deleted after each attempt (FR-11). Its fixtures are recorded live (`normal`, `readonly`, `badkey`).
- **Selection by measurement.** Among adapters that pass the suite, the default is the one that measures best on the same task, rules and plan (cost per success, then wall time), and the fallback is the next best. A candidate harness is measured against both before it is selected, and the ranking is re-run when one appears. No harness is the default by name.
- **Today's values.** Plan: GOAT through the Provider API. Default harness: **omp** (`base/stacks/omp.md` in poly-mind), plan profile `--approval-mode always-ask`, build `--approval-mode yolo`. Fallback: **opencode** (FRG-65), on the same Provider API. The Command Code CLI (`cmd`) is interactive-only for the owner; its runner adapter stays in the code, dormant, until opencode's adapter passes the conformance suite, then it may be removed. FRG-65 relaxes FRG-43's retired-harness guard (`TestNoRetiredHarnessIsNamed`), which named opencode as retired.

## Consequences

- Switching to another coding plan (OpenCode Go, OpenRouter, a vendor's own API) is a provider entry, a repository secret and a `runner plan-define` record. Switching to a better harness is an adapter that passes the conformance suite and a configuration line.
- Cost metering no longer trusts a harness's own figure: omp's charged peak rates off-peak (exactly 2x). The runner prices tokens from the plan's rate card, so FR-22's windows and NFR-1's cap meter correctly under any harness.
- Caching is the harness's job but measured by the runner: the stable rules prefix (FRG-50) must hold under each adapter. omp's static prompt precedes its per-directory block, so it caches across directories on its own.
- CI installs each enabled harness pinned (omp needs bun). The runner's GitHub App keeps no `workflows` permission; tickets that change workflows are built by hand (FRG-61).
- The fallback is not tied to one plan: opencode reaches any OpenAI-compatible plan, so a plan switch keeps both harnesses, where the Command Code CLI reached only the Command Code plan.
- **Unverified:** whether Command Code's terms allow GOAT to be reached from a third-party harness through its Provider API; recorded in `provider_plans`, not a gate (`adr/0014`).

## Alternatives

- **Keep `command-code/` as the only prefix and let omp serve it.** Smaller, but the plan stays welded to Command Code, and a second plan is a code change again.
- **omp only, no fallback.** Simpler; rejected, because a harness with no fallback stops the product when it fails.
- **The Command Code CLI as the fallback.** Already built; rejected because it measured dearest ($0.0184 per success against opencode's $0.0153) and reaches only the Command Code plan.
- **opencode as the default.** Measured second on cost; it is the fallback instead (FRG-65), until a harness measures better.
