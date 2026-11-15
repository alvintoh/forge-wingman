import { afterEach, describe, expect, it, mock } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import * as pageWindowModule from "../page-window";
import { DataSection } from "./data-table";
import type { PR, RewriteView, Tile } from "./types";

// A static render never pages, so a test that needs a later page names it here.
const realPageWindow = pageWindowModule.pageWindow;
let forcedPage: number | null = null;
mock.module("../page-window", () => ({
  pageWindow: (total: number, page: number, size: number) =>
    realPageWindow(total, forcedPage ?? page, size),
}));
afterEach(() => {
  forcedPage = null;
});

const pr: PR = {
  pr: 108,
  run: "run-2026-02-27-a",
  repo: "acme/widgets",
  size: "M",
  title: "Add the widget table",
  agent_lines: 520,
  owner_lines: 31,
  rate: "6.0%",
  rate_value: 0.06,
  merged_at: "2026-02-28T02:00:00Z",
  pr_url: "https://github.com/acme/widgets/pull/108",
  plan_model: "deepseek-v4-flash",
  build_model: "deepseek-v4-flash",
  review_model: "deepseek-v4-flash",
  trailing_10: null,
  trailing_10_value: null,
};

const earlier: PR = { ...pr, pr: 107, title: "Add the gadget table" };

const rewrite: RewriteView = {
  tiles: [],
  series: [],
  prs: [pr, earlier],
  by_size: [],
  by_model: { plan: [], build: [], review: [] },
  by_repo: [],
  bars: { guideline: 0.1 },
};

const markup = renderToStaticMarkup(<DataSection rewrite={rewrite} />).replaceAll("&#x27;", "'");
const table = /<table aria-labelledby="([^"]*)".*?<\/table>/s.exec(markup);
const head = /<thead.*<\/thead>/s.exec(table?.[0] ?? "")?.[0] ?? "";
const headRows = [...head.matchAll(/<tr.*?<\/tr>/gs)].map((m) => m[0]);
const headings = (row: string | undefined) =>
  [...(row ?? "").matchAll(/<th [^>]*>([^<]*)<\/th>/g)].map((m) => m[1]);

describe("the per-PR table", () => {
  it("is named by its caption, which reads Newest first", () => {
    const caption = new RegExp(`<p id="${table?.[1]}"[^>]*>([^<]*)</p>`).exec(markup);
    expect(caption?.[1]).toBe("Newest first");
    expect(markup).not.toContain("Per PR, newest first");
  });

  it("heads its second row with Title alone, and has no Plan, build, review header", () => {
    expect(headRows).toHaveLength(2);
    expect(headings(headRows[1])).toEqual(["Title"]);
    expect(headRows[1]?.match(/<t[dh] /g)).toHaveLength(1);
    expect(markup).not.toContain("Plan, build, review");
  });

  it("lists the rows as given beside a title cell of five columns and a lane cell of three", () => {
    expect(markup.indexOf("#108")).toBeGreaterThan(-1);
    expect(markup.indexOf("#108")).toBeLessThan(markup.indexOf("#107"));
    expect(markup).not.toContain("<button");
    expect(markup).toContain('<td colSpan="5"');
    expect(markup).toContain('<td colSpan="3"');
    expect(headRows[1]).toContain('colSpan="5"');
  });

  it("keeps the lanes' own words on a row and the note under the pager", () => {
    expect(markup).toContain(">Plan</dt>");
    expect(markup).toContain(">Build</dt>");
    expect(markup).toContain(">Review</dt>");
    expect(markup).toContain(">Plan, build and review are the strip's lanes in words.</p>");
  });
});

// Twelve PRs whose own lines sum to neither of the tile's figures, so a pinned row
// added up from the rows on screen cannot match it.
const twelve: PR[] = Array.from({ length: 12 }, (_, i) => ({
  ...pr,
  pr: 200 - i,
  pr_url: `https://github.com/acme/widgets/pull/${200 - i}`,
}));
const trailing: Tile = {
  prs: twelve.slice(0, 10).map((p) => p.pr),
  asked: 10,
  agent_lines: 5879,
  owner_lines: 719,
  rate: "12.2%",
  rate_value: 0.122,
  previous_rate_value: 0.141,
  trend: "down",
};
const lastFive: Tile = { ...trailing, asked: 5, agent_lines: 6002, owner_lines: 2293 };
const noLines: Tile = {
  ...trailing,
  asked: 1,
  agent_lines: 0,
  owner_lines: 40,
  rate: "no agent lines",
};

function render(view: Partial<RewriteView>) {
  return renderToStaticMarkup(<DataSection rewrite={{ ...rewrite, ...view }} />);
}

function pinnedBody(markup: string) {
  return /<table aria-labelledby=.*?(<\/thead><tbody[^>]*>.*?<\/tbody>)/s.exec(markup)?.[1] ?? "";
}

describe("the pinned Last 10 PRs row", () => {
  forcedPage = 1;
  const pageTwo = render({ prs: twelve, tiles: [trailing, lastFive, noLines] });
  forcedPage = null;
  const pinned = pinnedBody(pageTwo);

  it("leads the PRs on a later page with the trailing-ten tile's figures", () => {
    expect(pinned).toContain(">Last 10 PRs</span></th>");
    expect(pinned).toContain(">5,879</span>");
    expect(pinned).toContain(">719</span>");
    expect(pinned).toContain("12.2%");
    expect(pinned).not.toContain("#19");
    expect(pageTwo).toContain("#195");
    expect(pageTwo).not.toContain(">#200<");
  });

  it("sets its label and line figures heavier than a row's, over a darker rule", () => {
    expect(pinned).toMatch(/<th[^>]*class="[^"]*font-semibold[^"]*"[^>]*><span[^>]*>Last 10 PRs</);
    expect(pinned).toContain('<span class="font-mono font-semibold">5,879</span>');
    expect(pinned).toContain('<span class="font-mono font-semibold">719</span>');
    expect(pinned).toMatch(
      /<span class="[^"]*font-mono font-semibold[^"]*">(?:(?!<\/td>).)*12\.2%/,
    );
    expect(pageTwo).toContain('<span class="font-mono">520</span>');
    expect(pinned).toMatch(/^<\/thead><tbody class="[^"]*border-b-2 border-b-ink2/);
  });

  it("counts the PRs of a window shorter than ten, in its label and its note", () => {
    const short = render({ prs: twelve, tiles: [{ ...trailing, prs: [200, 199, 198] }] });
    expect(pinnedBody(short)).toContain(">Last 3 PRs</span></th>");
    expect(short).toContain(
      "Last 3 PRs totals the three newest PRs on every page; its rate is the Trailing 10 on the tile.",
    );
    expect(short).not.toContain("ten newest");
  });

  it("is left out, with its note, while the trailing-ten window has no PRs", () => {
    const empty = render({ prs: twelve, tiles: [{ ...trailing, prs: [] }] });
    expect(empty).not.toContain("Last 0");
    expect(empty).not.toContain("totals the");
    expect(empty).toContain("#200");
  });

  it("is red above the guideline only once its window is full, as the tile is", () => {
    const full = pinnedBody(render({ prs: twelve, tiles: [trailing] }));
    expect(full).toContain("text-stop");
    expect(full).toContain("(above 10%)");
    const short = pinnedBody(
      render({ prs: twelve, tiles: [{ ...trailing, prs: [200, 199, 198] }] }),
    );
    expect(short).toContain("12.2%");
    expect(short).not.toContain("text-stop");
    expect(short).not.toContain("above");
  });

  it("is named in the table's notes, which leave it out with no tile to pin", () => {
    const note =
      "Last 10 PRs totals the ten newest PRs on every page; its rate is the Trailing 10 on the tile.";
    expect(pageTwo).toContain(note);
    expect(markup).not.toContain(note);
    expect(markup).not.toContain("Last 10 PRs");
  });
});

describe("the trend arrows in words", () => {
  const view = render({ prs: twelve, tiles: [trailing, lastFive, noLines] });
  const cells = [...view.matchAll(/data-label="Trend arrow, in words"[^>]*><span>([^<]*)</g)].map(
    (m) => m[1],
  );

  it("carry each tile's trend and lines, and no lines for a window with none", () => {
    expect(cells).toEqual([
      "down from 14.1%, 719 of 5,879 agent lines",
      "down from 14.1%, 2,293 of 6,002 agent lines",
      "down from 14.1%",
    ]);
  });
});
