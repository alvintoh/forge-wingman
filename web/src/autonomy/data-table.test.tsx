import { describe, expect, it } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import { DataSection } from "./data-table";
import type { PR, RewriteView } from "./types";

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
