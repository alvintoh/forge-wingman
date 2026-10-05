# 0014 — The owner's plan records live in Firestore, written only by the owner CLI

- **Date:** 2026-10-04
- **Status:** accepted — built (FRG-32 part 1); the weekly refresh and the surface's write path are not built

## Context

Whether a provider's terms allow unattended use, how it bills and whether it can charge past its allowance are facts the owner reads from vendor pages. Admission needs them per provider before a run starts, and a ticket label can now name a model from any provider (FR-30).

## Decision

One document per provider, `provider_plans/<provider>`: the owner's definition (name, monthly price, billing as free, allowance or per-token, limit behaviour, harness and model pairs, the pages to refresh from), the terms verdict with the wording, source and date it rests on, the vendor replies, and an opt-in flag for per-token spend.

- **Only the owner CLI writes it** (`plan-define`, `plan-verdict`, `plan-reply`, `plan-optin`). No run job writes it, and the opt-in is written by `plan-optin` alone.
- **The dispatcher reads it at admission.** A provider with no billing recorded is unconfigured and its tickets are refused; a per-token provider not opted in is refused naming the opt-in. A failed read leaves the ticket unadmitted rather than guessing.
- **Changing a plan's billing clears its opt-in**, so consent given for one billing model cannot carry over to another.
- A free plan is recorded at a zero price; every other billing needs a positive one.

## Consequences

- The recorded verdict is evidence, never a gate: an unconfirmed verdict is shown, not enforced.
- The surface (FR-32) will need a narrow write path for ordering plans and editing their models. It is not built, it must never hold an API key, and the identity it writes with is still to be decided.
- The weekly refresh from the recorded pages is also not built.

## Alternatives

A config file in the repo (rejected: a plan edit would need a deploy and the surface could not write it) and per-run flags (rejected: nothing durable to admit against).
