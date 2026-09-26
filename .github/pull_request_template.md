**Ticket:** ref <TEAM-n> <!-- "ref", never "closes": Linear auto-closes on merge -->
<!-- PR title: the ticket key right after the prefix, e.g. `feat(runner): FRG-16 build …`.
     A squash makes the title the commit subject on main; no ticket, no key. -->

## Summary
<!-- First line: an imperative summary of WHAT changed. Then the problem, and why
     this approach. (Google eng-practices, "Writing good CL descriptions".) -->

## Verification
<!-- The acceptance criteria first, one row each copied from the ticket, then every
     other check that was run. Put a command a reviewer can re-run in the Check column.
     Any ❌ or ⏳ keeps this PR a DRAFT. -->

| Check | What it proves | Result |
|---|---|---|
| AC1 · <short name> | <test / command / read-back, and what it showed> | ✅ |
| `<other check>` | <what passing means> | ✅ |

## Screenshots
<!-- FRONTEND changes only — delete for back-end-only. /fe-sweep's engine × state
     matrix: the states a reviewer cannot cheaply reach, before and after. -->

## Notes
<!-- Known limitations, deliberate scope cuts, follow-ups. Delete if none. -->
