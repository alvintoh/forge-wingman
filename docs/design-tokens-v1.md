---
title: Forge Wingman — design tokens v1
date: 2026-09-21
tags: [design-tokens, forge-wingman, tailwind, css]
status: accepted
version: 1
---

# Forge Wingman — design tokens v1

> **This file deliberately restates NO token values.** That is not an omission —
> it is the whole design of this document, and the reason is below.

## What the build implements against

**`theme.css`** — generated, never edited. It is a Tailwind v4 `@theme` block, so
it is the literal file the stylesheet imports. It is generated into the vault's
`design/` and copied to the product repo's `web/src/`, beside the one file that
imports it.

```
design/tokens.py          the SOURCE          hand-maintained
   ├── gen_tokens.py   →  design/tokens-v1.svg      a projection (for a person)
   └── gen_theme_css.py → design/theme.css          a projection (for the build)
```

Change a token in `tokens.py` and re-run both generators. Nothing else declares a
value, so nothing else can disagree.

## Why this document holds no values

`/tech-design` §4 asks for the tokens "as text, in the names the chosen stack will
actually use", because a picture is not greppable and code cannot consume an SVG.
That instruction assumes **no tokens source exists yet**, which is true for most
products and false here — `/proto-draw` built `tokens.py` first, and that file's
own docstring forbids precisely what §4 asks for:

> *"a token document beside a generator that declares the same constants is two
> copies with nothing to detect the drift, so the document is the source and the
> drawing is a projection of it."*

Both are right. The reconciliation is the single-source rule: **one authoritative
copy, and a generated projection for every surface that cannot read it natively.**
A CSS file is such a surface — Tailwind cannot import a Python module — so the
projection is generated rather than transcribed.

So this document carries only what has **no other home**: the name mapping, the
stack-specific decisions, and the two deliberate absences. Every figure lives in
`tokens.py`; every measured contrast ratio lives on `tokens-v1.svg`, which
re-measures at draw time.

## Name mapping

Tailwind v4 is CSS-first, and the **namespace is load-bearing**: a custom property
declared as `--color-*` inside `@theme` makes Tailwind emit the matching
utilities. So the namespaces are chosen for that behaviour, not for tidiness.

| `tokens.py` | `theme.css` | Utilities you get |
|---|---|---|
| `GROUND`, `CARD`, `HAIR`, `TRACK` | `--color-ground` … | `bg-ground`, `border-hair` |
| `INK`, `INK2`, `MUTED`, `ACCENT` | `--color-ink` … | `text-ink`, `text-muted` |
| `STOP`, `DEFER`, `REVIEW`, `OK` | `--color-stop` … | `bg-stop`, `text-ok` |
| `DECIDE`, `IDENTITY` | `--color-decide`, `--color-identity` | the gate-class legend |
| `SANS`, `MONO` | `--font-sans`, `--font-mono` | `font-sans`, `font-mono` |
| `T` (7 steps) | `--text-hero` … `--text-micro` | `text-body`, `text-micro` |
| `GAP`, `PAD`, `PADX`, `BAND`, `ROW_H` | `--spacing-*` | `p-pad`, `gap-gap` |
| `PHONE_W`, `DESK_W` | `--container-phone`, `--container-desk` | `@container` queries |

**Names are the token's ROLE, lower-cased — never its value.** `tokens.py` records
why: `DECIDE` and `IDENTITY` were `VIOLET` and `TEAL` until 2026-09-21, and one of
them moved the same day, which is the naming rule's own example of what not to do.

**px → rem.** The generator divides by 16 and keeps the px figure in a trailing
comment. Values are authored in px because the surfaces are drawn in px; they ship
in rem so the type respects a reader's browser setting, which NFR-8's AA bar
expects.

## Two deliberate absences

**No radius tokens in the CSS — the scale lives in `tokens.py`.** `RADII` declares
four steps for the drawn surfaces and `sheetcheck.radii()` enforces them; two values
are RULES rather than steps — a **pill** or bar cap is `min(w, h) / 2`, and the app
icon's tile corner is `ICON_CORNER`, a fixed fraction of its size. `theme.css` emits
no `--radius` until a component reads one, and the generator **asserts** none leaked
in. *(Corrected 2026-09-23: this said no scale was declared and consolidation was a
follow-up — true on 2026-09-21, superseded the same day by `RADII`.)*

**No contrast figures.** They appear in neither `tokens.py` nor here. `gen_tokens.py`
re-measures every ratio when it draws the sheet, so each has exactly one home. A
figure written into a comment is a hand-synced copy of a measurement, and it went
stale exactly that way: `INK` carried "16.1:1 on card" and measures 17.74 on card
and 16.69 on the page — matching neither, undetected until the sheet re-measured it.

## What the generator asserts

Read off the **emitted CSS**, never off the generator's own lists — a check whose
input you also control is not a check:

1. **Every colour token in `tokens.py` reaches `theme.css`.** This is the one drift
   a projection can still suffer: a token added to the source and forgotten in the
   emit list. Mutation-tested 2026-09-21 — dropping one token names exactly that
   token, and the unmodified generator passes.
2. **The type ramp is closed** — as many `--text-*` rules as entries in `T`, no
   more. Nothing outside the ramp is permitted, which is why it ships whole rather
   than inheriting Tailwind's default scale.
3. **No `--radius` leaked in**, per the absence above.

## Target size, and why it is a token

`TARGET = 44` is the floor for any control that is **not** a full-width row.
`ROW_H = 106` already cleared it for inbox rows, which is why the comment beside
it says so — but nothing covered the smaller controls, and that gap had a
consequence.

- **NFR-8 requires WCAG 2.2 level AA**, which includes **2.5.8 Target Size
  (Minimum)** at 24x24 CSS px. None of its exceptions — spacing, inline,
  user-agent, essential — covers a dialog dismiss.
- **Apple HIG says 44pt**, and `proto-draw`'s authority order puts Apple above
  Material on spatial questions. So **44 is the token; 24 is the legal minimum
  it clears**, not the target.
- **The glyph is not the target.** The dialog's close mark is an 11px x centred
  inside a 44px hit area. Drawing the bound rather than implying it is what makes
  the prototype state the requirement instead of leaving it to be inferred.

*Added 2026-09-21, found by `/sync-docs` pairing NFR-8 against a drawing changed
the same day: the close affordance had been drawn as an 11px glyph with no hit
area at all. Neither document mentioned the other, which is the signature of this
defect class rather than an incidental — and with no token here, nothing
downstream would have caught it either.*

## Beyond tokens: what `theme.css` also carries

Two rules that are not tokens but belong with them, because both are **required**
rather than stylistic:

- **`:focus-visible`** uses the accent at the stroke width `tokens.py` measured
  against WCAG **2.4.11** — `adr/0001` records the calculation (3px at inset 6
  gives 4,062px² against the 2,804px² a 2px perimeter requires).
- **`prefers-reduced-motion`** suppresses animation and transitions, and
  **deliberately does not suppress focus**, because focus is functional feedback
  rather than decoration. That exception is `tokens.py`'s `MOTION["reduced"]`,
  stated there and implemented here.

## Pointers

| For | Read |
|---|---|
| the values | `design/tokens.py` |
| the reasoning, with a status | `adr/0001-the-design-language.md` |
| the measured ratios | `design/tokens-v1.svg` |
| the surfaces in use | `design/inbox-desktop-v1.svg`, `design/inbox-mobile-v1.svg` |
| why a SPA and not Next.js | `adr/0006-vite-spa-not-nextjs.md` |
