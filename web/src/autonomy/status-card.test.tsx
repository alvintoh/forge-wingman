import { describe, expect, it, mock } from "bun:test";
import { createElement, type ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import type { MergeState } from "./types";

// A static render has no router, so the settings link renders as a plain anchor.
mock.module("@tanstack/react-router", () => ({
  Link: ({ to, children }: { to: string; children?: ReactNode }) =>
    createElement("a", { href: to }, children),
}));
const { MergeStatusCard } = await import("./status-card");

const merge: MergeState = {
  switch_on: true,
  rate_allows: true,
  rate_value: "7.4%",
  limit: "10.0%",
  window: 10,
  effective: true,
  reason: "on",
};

describe("MergeStatusCard", () => {
  it("states the pooled rate with no all-projects label", () => {
    const markup = renderToStaticMarkup(createElement(MergeStatusCard, { merge }));
    expect(markup).toContain("rewrite rate over the trailing 10 PRs is 7.4%");
    expect(markup).not.toContain("all projects");
  });
});
