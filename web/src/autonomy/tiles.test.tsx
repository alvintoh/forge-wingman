import { afterEach, describe, expect, it, mock } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import type { PR, Tile } from "./types";

// A static render never measures, so the row's width is set here; 0 is what an
// unmeasured row reports, which renders no tiles, as the real hook does.
let width = 0;
mock.module("./use-width", () => ({
  useWidth: () => ({ ref: { current: null }, width }),
}));
const { Tiles } = await import("./tiles");

afterEach(() => {
  width = 0;
});

const pr: PR = {
  pr: 175,
  run: "run-2026-11-01-a",
  repo: "alvintoh/forge-wingman",
  size: "M",
  title: "Add the widget table",
  agent_lines: 520,
  owner_lines: 31,
  rate: "6.0%",
  rate_value: 0.06,
  merged_at: "2026-11-01T02:00:00Z",
  pr_url: "https://github.com/alvintoh/forge-wingman/pull/175",
  plan_model: "deepseek-v4-flash",
  build_model: "deepseek-v4-flash",
  review_model: "deepseek-v4-flash",
  trailing_10: null,
  trailing_10_value: null,
};

// A second repository among the PRs, so the tile names the repository too.
const other: PR = { ...pr, pr: 12, repo: "alvintoh/forge-vault", pr_url: "" };

const tile = (prs: number[]): Tile => ({
  prs,
  asked: prs.length,
  agent_lines: 1234,
  owner_lines: 70,
  rate: "5.7%",
  rate_value: 0.057,
  previous_rate_value: 0.058,
  trend: "down",
});

const tiles = [tile([175, 12]), tile([175]), tile([175])];

function lastTile(prs: PR[], at: number) {
  width = at;
  const markup = renderToStaticMarkup(<Tiles tiles={tiles} prs={prs} bar={0.1} />);
  return /<li[^>]*data-tile="last PR".*?<\/li>/s.exec(markup)?.[0] ?? "";
}

describe("the last-PR tile", () => {
  for (const [layout, at] of [
    ["beside its words", 900],
    ["stacked", 300],
  ] as const) {
    describe(layout, () => {
      const markup = lastTile([pr, other], at);

      it("ends with the PR link, after the trend and the agent lines", () => {
        const link = markup.indexOf('<a href="https://github.com/alvintoh/forge-wingman/pull/175"');
        expect(link).toBeGreaterThan(markup.indexOf("down"));
        expect(link).toBeGreaterThan(markup.indexOf("1,234 agent lines"));
        expect(markup.indexOf("</a>")).toBe(markup.lastIndexOf("</p></li>") - "</a>".length);
      });

      it("keeps the number's row free of the link", () => {
        const figure = /<p[^>]*>(?:(?!<\/p>).)*data-number.*?<\/p>/s.exec(markup)?.[0] ?? "";
        expect(figure).not.toContain("<a ");
      });

      it("keeps the link a 44px target that opens a new tab and says so", () => {
        expect(markup).toContain('target="_blank"');
        expect(markup).toMatch(/<a [^>]*class="[^"]*min-h-11 min-w-11[^"]*"/);
        expect(markup).toContain("(opens in a new tab)");
      });
    });
  }

  it("breaks the repository after its slash", () => {
    expect(lastTile([pr, other], 300)).toContain("alvintoh/<wbr/>forge-wingman");
  });

  it("shows a PR whose address cannot be checked as plain text in the same last row", () => {
    const markup = lastTile([{ ...pr, pr_url: "" }, other], 900);
    expect(markup).not.toContain("<a ");
    expect(markup).toMatch(
      /<p[^>]*><span[^>]*>#175<span[^>]*>alvintoh\/<wbr\/>forge-wingman<\/span><\/span><\/p><\/li>$/,
    );
    expect(markup.indexOf("#175")).toBeGreaterThan(markup.indexOf("1,234 agent lines"));
  });
});
