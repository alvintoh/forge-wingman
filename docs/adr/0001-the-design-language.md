# 0001 — The design language: a triage surface, ranked by colour

- **Date:** 2026-09-21
- **Status:** accepted
- **Scope:** Forge Wingman's **surfaces** — the inbox, the statistics view and
  their dialogs. **NOT its diagrams.** A diagram about the product is a vault
  document and uses the vault's house style (Segoe UI + Cascadia Mono on
  `#E6E6E2`). Forge Octant's own design-language record states the same split
  from its side, as does the career-direction product's.

> **This record exists because the reasoning was living in comments.**
> The comment-density rules are explicit that a doc describes what a thing IS,
> and that design *rationale* belongs elsewhere — a decision record carries a
> status and can be superseded, which a comment cannot. Every argument below was
> sitting in `design/tokens.py` or `design/gen_run_surface.py`, where nothing
> could supersede it and no ticket could cite it.

## Context

The product is an **inbox you check while doing something else** — runs happen
unattended and this surface is where you find what stopped. That framing decides
more than it looks: it makes the surface a *triage queue* rather than a
dashboard, and triage has one question, *what do I look at first?*

## Decision

### Colour RANKS; the word CLASSIFIES

A fully semantic scale was built first — green ready, blue needs-a-value, violet
decide, amber waiting, red broken, teal guard-held. Every pairing was defensible
and it measured clean against every contrast rule. **It was reverted.** Spread
across five hues, an inbox of five rows reads as a palette rather than a queue,
and nothing looks urgent. The owner's words: *"nothing is urgent and looks
weird"*.

So red and amber carry the stops, blue carries what is merely ready, and the
pill spells out what a thing IS in words. **WCAG 1.4.1 is satisfied by the word,
not by the hue** — which is what frees colour to do the other job.

**Consequence worth stating: two tags may share a colour.** `TODO` and `BUDGET`
are both `DEFER`, because they are the same urgency and different things. That
is the rule working, not a collision.

### The gate legend is a CATEGORICAL set, and lives by different rules

`stats_body` breaks runs down by which gate class stopped them — five distinct
refusals (FR-7..FR-11), where two sharing a colour would assert they are the same
kind of stop. That is a *categorical* legend, not a ranking, so it takes five
distinguishable hues and the ranking argument above does not apply to it.

### Tokens are a SOURCE, and every drawing is a projection of it

`design/tokens.py` holds the values; `gen_run_surface.py` imports them and
declares none. `tokens-v1.svg` and `components-v1.svg` are drawn *from* it, and
the token sheet re-measures every contrast ratio at draw time.

A separate token artifact is established practice — the W3C Design Tokens
Community Group's format reached its first stable version (2025.10) on exactly
this premise. It is Python here rather than that group's JSON because the
consumers are Python generators; JSON is the upgrade path the day a design tool
needs to read it.

**Components are NOT tokens.** A loader, a toast and a button are compositions
with structure and states, and no token format can express one. They live on
`components-v1.svg`, and it calls the same functions the surfaces call — a
component sheet that redraws its own button is a picture of a button.

### Motion: Material 3's `standard` scheme

`/proto-draw` §1 grants Material 3 authority over motion, with WCAG overriding.
M3 has **replaced** its duration tokens with a spring system
(`md.sys.motion.spring.<fast|default|slow>.<spatial|effects>`) in two schemes;
the old short/medium/long/emphasized durations are legacy and now govern
transitions only.

**`standard` over M3's own default `expressive`**, in Material's own words *"for
utilitarian products"* — this is a tool opened every morning, and ambient motion
is charming on first view and noise on the fortieth. What never animates: the
inbox list, the severity colours, and anything on load. `prefers-reduced-motion`
disables everything except focus, which is functional feedback.

### The app icon: a jet on a hexagon

*Added 2026-09-23.* A white delta jet cut into a pointy-top hexagon, on the ONE
accent. Drawn by `design/gen_icon.py` as a single geometry that produces
`icon.svg`, `favicon.ico` and every surface header's mark, so the tab and the
screens cannot disagree. The tile corner is `ICON_CORNER`, a ratio rather than a
radius step.

**Chosen after seven rounds, judged at 16px.** Rejected, so none is re-proposed:
stroked chevrons (a CSS "back to top" arrow), curved blades (leaves), a tapered W,
a wing over a dot (an eye, a crescent moon), a top-down plane (airplane mode),
a W on a disc or leaning (WordPress, Webflow).

**Known resemblance, accepted:** a white delta on a hexagon is close to Angular's
logo. Accepted because this is a single-user tool behind IAP, the colour is this
product's accent rather than Angular's, and the jet has no crossbar. **Revisit if
the product ever faces the public.**

## Consequences

**A colour collision shipped and was caught by eye, not by a gate.** The gate
class for identity was `#0F766E` and `OK` is `#047857` — **1.00:1 against each
other**, CIEDE2000 ΔE 9.16, the same colour to the eye. They co-occur:
`stats_body` draws the rewrite rate in `OK` and the gate legend on one panel.

`check-design` measures every colour against the **grounds** and never against
its siblings, so two values indistinguishable from one another are structurally
invisible to it. **And no threshold can close that gap**: ΔE 9.16 sits *between*
`STOP`/`DEFER` at 9.56 and the house palette's `MUTE`/`ARROW` at 7.71, both of
which are correct. A checker calibrated to catch the broken pair would fire on
two working ones and be ignored. **So this class of defect is found by putting
the colours side by side on the component sheet** — which is what found it.

Resolved to `#BE185D`, chosen by measuring every candidate against all six peers
**and** the three inks: worst ΔE 19.89. `slate #334155` measured best against the
peers and was rejected on the second pass at ΔE 7.42 from `INK2`, the ink it
shares a card with — *worse than the collision being fixed*.

**Gate colours are named for the CLASS, not the hue** — `DECIDE` and `IDENTITY`,
renamed from `VIOLET` and `TEAL` the same day one of those values moved, which is
the naming rule's own example of why.

**A component drawn twice has already drifted.** Three instances in one session:
chip widths hand-typed per label; the button written four times with two
paddings; the count badge at 15px on the phone and a bare 11px number on the
desktop. Each was found by needing to draw the thing once, in isolation.

## Open

- ~~**Twelve corner radii**~~ **Partly closed 2026-09-21; the consolidation is
  still open and is the owner's call.** Re-measured, and the count was WRONG in
  both directions: sixteen distinct values were in use, not twelve — and most
  were never scale members. A pill or a rounded bar cap is `min(w, h) / 2`, which
  is a RULE; counting those as steps is what made a coherent design read as
  undisciplined. Split by measurement: **nine derived, nine chosen**, and only
  the second half is a scale.

  ⚠️ *The measurement itself was wrong first, in a way worth recording: testing
  `r == h / 2` misfiled 240 vertical bars — capped on their WIDTH — as deliberate
  choices. `min(w, h)` covers both axes. A measurement that invents a problem is
  worse than none, because the problem looks real.*

  **`tokens.py` now declares `RADII` with a role per step**, and
  `sheetcheck.radii()` asserts every chosen radius is one — so the record's own
  complaint, that no scale was declared, is answered. It records the values
  ALREADY IN USE rather than redesigning them.

  **What is still open is the redundancy**, which is measured and not acted on:
  `14 · 16 · 18 · 20` are four steps inside 6px, on containers no reader can tell
  apart by their corners. Collapsing them changes how every surface looks, which
  makes it a taste decision rather than a tidy-up. *(The sibling product measures
  worse — `5 · 6 · 7 · 8` inside 3px — and is recorded the same way.)*

- ~~**The keyboard model lives in a docstring.**~~ **Closed 2026-09-21 by
  `prd-v1` FR-25 and NFR-8.** The APG listbox pattern the focus ring implies —
  one tab stop, arrows between rows, Enter opens the sheet, Escape returns focus
  — is now a falsifiable requirement rather than a docstring, and NFR-8 records
  the **WCAG 2.2 AA** bar this record had been measuring against with nothing
  asking it to. FR-25 is scoped to the INBOX: the statistics tab is read-only and
  FR-20's phone path has no arrow keys, so a requirement written across "the run
  surface" would have been false on the device FR-20 exists to include. The
  dialogs' focus trap remains uncovered by any FR and answers to NFR-8 alone.

## Note on this folder

`adr/` follows Forge Octant, the nearest peer, and `/tech-design`'s own
vocabulary. **The other product uses `decisions/` for the same artifact**, and the
vault root uses `decisions/` for vault-level records. That split is a small, real
drift and is recorded here rather than silently resolved.
