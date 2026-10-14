# 0014 — The owner's plan records live in Firestore, written only by the owner CLI

- **Date:** 2026-10-04
- **Status:** accepted — built (FRG-32 part 1), and the refresh from a plan's recorded pages is built as the owner command `runner plans-refresh`; the weekly schedule that runs it and the surface's write path are not built

## Context

Whether a provider's terms allow unattended use, how it bills and whether it can charge past its allowance are facts the owner reads from vendor pages. Admission needs them per provider before a run starts, and a ticket label can now name a model from any provider (FR-30).

## Decision

One document per provider, `provider_plans/<provider>`: the owner's definition (name, monthly price, billing as free, allowance or per-token, limit behaviour, harness and model pairs, the pages to refresh from), the terms verdict with the wording, source and date it rests on, the vendor replies, an opt-in flag for per-token spend, and the harnesses the owner has opted in on that plan.

- **Only the owner CLI writes it** (`plan-define`, `plan-verdict`, `plan-reply`, `plan-optin`, and `plans-refresh` for a plan's dated facts subcollection alone). No run job writes it; the spend opt-in is written by `plan-optin` alone, and the harness opt-in by `plan-optin -harness` alone.
- **A harness that is not open-source runs only where the plan record can be read**: build and PR-review jobs hold no plan record and never read opt-ins, so they select none of them and move to the plan's next harness, while the commands that run a harness for the owner (`plan-smoke`, `review-smoke`) read the plan's opt-in and honour it.
- **The dispatcher reads it at admission.** A provider with no billing recorded is unconfigured and its tickets are refused; a per-token provider not opted in is refused naming the opt-in. A failed read leaves the ticket unadmitted rather than guessing.
- **Changing a plan's billing clears its opt-in**, so consent given for one billing model cannot carry over to another.
- A free plan is recorded at a zero price; every other billing needs a positive one.

## Consequences

- The recorded verdict is evidence, never a gate: an unconfirmed verdict is shown, not enforced.
- The surface (FR-32) will need a narrow write path for ordering plans and editing their models. It is not built, it must never hold an API key, and the identity it writes with is still to be decided.
- The refresh from the recorded pages is built as the owner command `runner plans-refresh`, which reads the vendor's pages, its model list, the Coding leaderboard and the free feed into one dated facts document per plan and decides nothing; the weekly schedule that runs it is not built, so nothing reads a plan's sources on its own yet.

## Alternatives

A config file in the repo (rejected: a plan edit would need a deploy and the surface could not write it) and per-run flags (rejected: nothing durable to admit against).
