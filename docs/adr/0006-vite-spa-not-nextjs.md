# 0006 — A Vite SPA served by the Go binary, not Next.js

- **Date:** 2026-09-21
- **Status:** accepted

## Context

`engineering-defaults`' Core Stack records *"TypeScript, React, Next.js App Router,
TanStack (Router, Start, Query)"*.

**React is not in question** — it is required by requirement. FR-24 retains an
unsubmitted note per row, restores it when the sheet reopens, keeps it
session-scoped and deliberately loses it on reload; FR-25 wants a roving-tabindex
listbox where Escape returns focus to the originating row *and keeps that note*.
That is the browser holding state between saves.

It also disqualifies the server-rendered route on its own terms: the Go stack pack
records that **htmx does not handle keyboard shortcuts**, so FR-25's focus model
would be hand-written JS regardless and htmx would be replacing a `fetch` call with
an attribute while the hard part stayed manual.

So the open question was only ever **Next.js, or a client-only SPA served by Go**.
The first framing of this decision claimed Next.js *"deletes the API contract"* via
server components. That was wrong, and correcting it reversed the decision:
**Next.js does not remove the seam, it relocates it to the database schema**, where
the TypeScript side needs its own client and types and one migration must be
reflected in two bindings. `engineering-defaults`' polyglot note names exactly that
cost — *"a migration can break it silently."*

| | Next.js | Vite SPA + Go |
|---|---|---|
| image / cold start | ~150 MB, ~1-3s | **~30 MB, ~0.2s** |
| seam | **two DB bindings**, one schema | one binding; 3 typed endpoints |
| server languages | Go + TS | **Go only** |
| build toolchain | Node + Go | Node + Go — *same* |
| functionality | tie | tie |

**Next.js has a third form this table originally left out: static export.**
*(Added 2026-09-23.)* With `output: 'export'`, `next build` writes plain HTML/CSS/JS to
`out/` and needs no Node server, so it would embed in `embed.FS` exactly as a Vite
build does, and the image and cold-start rows above stop distinguishing the two.
Next.js's own docs price it: *"Next.js server features are not supported with static
exports."* What that leaves is a framework whose server half is switched off,
against Vite + TanStack Router at a fraction of the dependency tree for three
routes. **Vite still wins, on toolchain weight rather than runtime weight.**
Two neighbours were checked the same day and change nothing: **TanStack Start**'s SPA
mode adds server functions in TypeScript, and the server here is Go; **Vite+**
(VoidZero, MIT) wraps this same Vite and is at 1.0 RC. Revisit it at 1.0, as a
toolchain swap rather than a framework decision.

Functionality is a genuine tie: behind IAP there is no SEO, one user means no
first-paint pressure, thousands of rows mean no data volume, and nothing streams.

## Decision

**A Vite SPA — React + TanStack Router + TanStack Form — built to static assets and
embedded into the Go surface binary with `embed.FS`**, served alongside three JSON
endpoints.

- **TanStack Form** rather than React 19 form actions, on `engineering-defaults`'
  own instruction: *"a client-only SPA loses PE entirely and should default to
  TanStack Form instead."* Progressive enhancement is worth nothing on an
  IAP-fronted single-operator tool, and that file says so too.
- **Tailwind v4 via `@tailwindcss/vite`**, installed with Bun — Tailwind calls the
  plugin "the most seamless way to integrate it". Node stays a build-time dependency
  and never a runtime one either way. *(Corrected 2026-09-23: this said the standalone
  CLI, which Tailwind recommends only where a project has no `package.json` — and
  `web/` has one for Vite. `stacks/tailwind.md` now states the split.)*

## Consequences

- **One deployable serves the whole surface**, and it is a single static binary
  containing its own frontend.
- **The cold start is user-visible and this is where it pays.** The tool is opened
  once a morning behind IAP, so effectively *every* visit is a cold start. 0.2s
  against ~2s is felt daily.
- **The seam is narrow, explicit and versionable** — three endpoints, with TS types
  generated from the Go structs. One DB binding, in Go.
- **The cost, stated plainly:** those three endpoints are hand-written and
  maintained, where a server component would have queried the store directly.
- **No Node at runtime anywhere**, which also keeps the Cloud Run image small enough
  that the surface's own resource line stays negligible.
- **Deliberately NOT a reason:** cost. The surface scales to zero and takes a few
  requests a day, so it is near-free either way. This decision is about latency,
  seam quality and one server language — not the bill. The charged components are
  the runner's minutes and the model's tokens, and neither is affected.
