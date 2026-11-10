import { describe, expect, it } from "bun:test";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import autonomy from "../../fixtures/autonomy.json";
import { isAutonomy } from "../autonomy/guard";
import { RewriteRateView } from "./rewrite-rate";

describe("RewriteRateView", () => {
  it("draws the pooled rate with no scope line", () => {
    if (!isAutonomy(autonomy)) {
      throw new Error("the Autonomy fixture no longer matches the endpoint's shape");
    }
    const markup = renderToStaticMarkup(
      createElement(RewriteRateView, {
        read: { status: "ready", body: autonomy, url: "/api/autonomy" },
      }),
    );
    expect(markup).toContain(
      "Rewrite rate over the trailing 10 PRs a human merged is 7.4%; bar 20%",
    );
    expect(markup).not.toContain("all projects");
  });
});
