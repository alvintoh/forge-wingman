import { describe, expect, it } from "bun:test";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import type { RunningRun } from "./run";
import { LoadingRows, RunningList } from "./section";

const NOW = Date.UTC(2026, 2, 1, 12, 0, 0);

function run(ticket: string, minutes: number, limit: number, progress?: unknown): RunningRun {
  return {
    ticket,
    repo: "acme/widgets",
    stage: "building",
    claimed_at: new Date(NOW - minutes * 60_000).toISOString(),
    title: "",
    limit_minutes: limit,
    ...(progress === undefined ? {} : { progress }),
  };
}

function draw(runs: RunningRun[], count = runs.length): string {
  return renderToStaticMarkup(
    createElement(RunningList, { runs, count, now: NOW, named: false, projects: [] }),
  );
}

type Caption = { title: string; classes: string; html: string };

function captions(markup: string): Caption[] {
  return [...markup.matchAll(/<p title="([^"]*)" class="([^"]*text-cap[^"]*)">(.*?)<\/p>/g)].map(
    (m) => ({ title: m[1], classes: m[2], html: m[3] }),
  );
}

type Cell = { classes: string; width: number; text: string };

function fills(markup: string): string[] {
  return [...markup.matchAll(/<div class="(h-full[^"]*)" style="width:\d+%">/g)].map((m) => m[1]);
}

function figures(markup: string): Cell[] {
  return [
    ...markup.matchAll(
      /<span class="([^"]*text-label[^"]*)" style="width:(\d+)ch">([^<]*)<\/span>/g,
    ),
  ].map((m) => ({ classes: m[1], width: Number(m[2]), text: m[3] }));
}

function times(markup: string): Cell[] {
  return [
    ...markup.matchAll(/<span class="([^"]*text-cap[^"]*)" style="width:(\d+)ch">([^<]*)<\/span>/g),
  ].map((m) => ({ classes: m[1], width: Number(m[2]), text: m[3] }));
}

describe("RunningList fill", () => {
  it("is accent at 79 percent and defer from 80", () => {
    const [at79, at80] = fills(draw([run("A", 79, 100), run("B", 80, 100)]));
    expect(at79).toContain("bg-accent");
    expect(at79).not.toContain("bg-defer");
    expect(at80).toContain("bg-defer");
    expect(at80).not.toContain("bg-accent");
  });

  it("holds a minimum width only once the run has used a percent", () => {
    const [empty, started] = fills(draw([run("A", 0, 100), run("B", 1, 100)]));
    expect(empty).not.toContain("min-w-0.5");
    expect(started).toContain("min-w-0.5");
  });
});

describe("RunningList figure", () => {
  it("is defer text only past the limit", () => {
    const [atLimit, past] = figures(draw([run("A", 90, 90), run("B", 91, 90)]));
    expect(atLimit.classes).toContain("text-ink");
    expect(atLimit.classes).not.toContain("text-defer");
    expect(past.classes).toContain("text-defer");
    expect(past.classes).not.toContain("text-ink");
  });

  it("is drawn right-aligned until the row has been measured", () => {
    const [figure] = figures(draw([run("A", 5, 90)]));
    expect(figure.classes).toContain("text-right");
    expect(figure.classes).not.toContain("text-left");
  });
});

describe("RunningList columns", () => {
  it("sizes every row's figure and time cells alike, from the widest run", () => {
    const markup = draw([run("A", 12, 90), run("B", 113, 90)]);
    expect(figures(markup).map((cell) => cell.width)).toEqual([12, 12]);
    expect(times(markup).map((cell) => cell.width)).toEqual([11, 11]);
  });

  it("sizes the drawn rows from the runs folded away too", () => {
    const markup = draw([run("A", 12, 90), run("B", 113, 90)], 1);
    expect(figures(markup)).toHaveLength(1);
    expect(figures(markup)[0].text).toBe("13%");
    expect(figures(markup)[0].width).toBe(12);
    expect(times(markup)[0].width).toBe(11);
  });
});

describe("RunningList line", () => {
  it("lets the bar take the free room and holds the drawn column and stack gaps", () => {
    const markup = draw([run("A", 5, 90)]);
    expect(markup).toContain("grow-10000 basis-24");
    expect(markup).toContain("gap-x-3 gap-y-2");
  });
});

describe("RunningList caption", () => {
  it("draws one caption under each bar with the newest report's counts and age", () => {
    const markup = draw([
      run("A", 5, 90, { phase: "build", steps: 14, lines: 128, age_seconds: 126 }),
      run("B", 6, 90, { phase: "", steps: 3, lines: 0, age_seconds: 30 }),
      run("C", 7, 90, null),
    ]);
    expect(captions(markup).map((c) => c.html.replace(/<[^>]*>/g, ""))).toEqual([
      "Step 14 · 128 lines · 2m ago",
      "Step 3 · 0 lines · just now",
      "No progress yet",
    ]);
  });

  it("colours a stale age defer and leaves a recent one plain", () => {
    const [stale, fresh] = captions(
      draw([
        run("A", 5, 90, { phase: "build", steps: 12, lines: 40, age_seconds: 1260 }),
        run("B", 6, 90, { phase: "build", steps: 14, lines: 128, age_seconds: 126 }),
      ]),
    );
    expect(stale.html).toContain('<span class="text-defer">no update for 21m</span>');
    expect(fresh.html).not.toContain("text-defer");
  });

  it("reads a malformed report as no progress and keeps the other rows", () => {
    const markup = draw([
      run("A", 5, 90, { phase: "build", steps: 14, lines: 128, age_seconds: 126 }),
      run("B", 6, 90, { phase: "build", steps: "12", lines: 40, age_seconds: 60 }),
      run("C", 7, 90, { phase: "build", steps: 7, lines: 0, age_seconds: 140 }),
    ]);
    expect(captions(markup).map((c) => c.title)).toEqual([
      "Step 14 · 128 lines · 2m ago",
      "No progress yet",
      "Step 7 · 0 lines · 2m ago",
    ]);
  });

  it("cuts the caption at one line and titles it with the full words", () => {
    const [only] = captions(
      draw([run("A", 5, 90, { phase: "build", steps: 41, lines: 286, age_seconds: 95 })]),
    );
    expect(only.classes).toContain("truncate");
    expect(only.title).toBe("Step 41 · 286 lines · 1m ago");
  });
});

describe("LoadingRows", () => {
  it("holds the caption slot under every skeleton bar", () => {
    const markup = renderToStaticMarkup(createElement(LoadingRows));
    const slots = markup.match(/<span class="mt-1 block h-4 w-40 rounded-tag bg-track"><\/span>/g);
    expect(slots).toHaveLength(3);
  });
});
