# shots

Captures the built surface at four widths from fixture data, so a run that builds a surface PR can
show what it built without reading the live surface, which is private.

```sh
cd web
bun run build          # writes web/dist
bun run shots          # writes shots/ at the repository root
bun run shots --routes /stats,/config --out /tmp/shots
```

`bun run shots` is the web package's script: it runs this directory's own, so install this tool's
dependencies and its browser once:

```sh
cd tools/shots
bun install
bunx playwright install chromium
```

## What it does

1. Serves `web/dist` with a fixture api: every `/api` route answers the file of its path under
   `web/fixtures`, so nothing the app reads is live. A route with no fixture is answered 404 and
   recorded, and the run exits non-zero naming it.
2. Captures each route at 440x763 (scale 3), 768x1024, 1024x768 and 1280x800, at its full
   viewport, as `<route>-<s|m|l|xl>.png` — `/` is `index`, and a further segment joins with `-`
   (`/runs/abc123` is `runs-abc123-s.png`).
3. Refuses a shot, removing the file the band would have written, while
   - the app logged a failed read (`inboxNotRead` and its siblings), so the page is its error state,
   - the fonts have not loaded, or anything is `aria-busy`,
   - a control is under the pointer, or the page scrolls sideways.

`--routes` defaults to the routes whose page file changed since `--base` (default `origin/main`),
and to every route when no page changed. Run `bun run shots --help` for the rest.

## Determinism

Two runs over one build write byte-identical PNGs: the clock is fixed, animation and transition are
disabled, and the locale, timezone, colour scheme and device pixel ratio are pinned.

The Chromium that renders them is pinned twice, and both are needed. `playwright` is pinned to one
exact version, because each Playwright release bundles one Chromium build; `bun.lock` then holds
every dependency beneath it. **Commit the lockfile `bun install` writes on first install**, and
install with `bun install --frozen-lockfile` wherever the shots are produced — without the lock, a
caret range on any dependency can move the browser under a run that looked reproducible.
