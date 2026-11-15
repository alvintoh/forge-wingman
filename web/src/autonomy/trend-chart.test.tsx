import { describe, expect, it, mock } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import type { Point, PR } from "./types";

mock.module("./use-width", () => ({
  useWidth: () => ({ ref: { current: null }, width: 1100 }),
}));
const { TrendCard } = await import("./trend-chart");

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
  trailing_10: "6.0%",
  trailing_10_value: 0.06,
};
const point: Point = {
  repo: pr.repo,
  pr: pr.pr,
  merged_at: pr.merged_at,
  rate: "6.0%",
  rate_value: 0.06,
};

describe("how this is measured", () => {
  const markup = renderToStaticMarkup(
    <TrendCard series={[point]} prs={[pr]} bar={0.2} aside="all repositories" />,
  );

  it("says both counts are totalled over the window, so a larger PR weighs more", () => {
    expect(markup).toContain(
      "over the lines the runner wrote. Both are totalled over the last ten merged PRs, so a larger PR weighs more.",
    );
    expect(markup).not.toContain("pooled over the last ten merged PRs");
  });
});
