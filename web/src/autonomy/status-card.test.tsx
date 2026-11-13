import { describe, expect, it } from "bun:test";
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from "@tanstack/react-router";
import { renderToString } from "react-dom/server";
import { MergeStatusCard } from "./status-card";
import type { MergeState } from "./types";

const merge: MergeState = {
  switch_on: true,
  rate_value: "7.4%",
  guideline: "20.0%",
  window: 10,
  effective: true,
  reason: "on",
  rate_reason: "on",
  escaped_pr: 0,
  escaped_repo: "",
  escaped_by: 0,
  escaped_reason: "",
  escape_days: 7,
};

// The settings pill is a router Link, so the card renders inside a one-route memory router.
async function card(): Promise<string> {
  const root = createRootRoute({ component: () => <MergeStatusCard merge={merge} /> });
  const router = createRouter({ routeTree: root, history: createMemoryHistory() });
  await router.load();
  return renderToString(<RouterProvider router={router} />);
}

describe("MergeStatusCard", () => {
  it("states the pooled rate with no all-projects label", async () => {
    const markup = await card();
    expect(markup).toContain(
      "the trailing rewrite rate is 7.4% over the last 10 PRs a human merged, at or under the 20% guideline",
    );
    expect(markup).not.toContain("all projects");
  });
});
