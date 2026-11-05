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
  rate_allows: true,
  rate_value: "7.4%",
  limit: "10.0%",
  window: 10,
  effective: true,
  reason: "on",
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
    expect(markup).toContain("rewrite rate over the trailing 10 PRs is 7.4%");
    expect(markup).not.toContain("all projects");
  });
});
