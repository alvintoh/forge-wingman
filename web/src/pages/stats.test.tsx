import { describe, expect, it } from "bun:test";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from "@tanstack/react-router";
import { renderToString } from "react-dom/server";
import { validateProjectSearch } from "../project-search";
import { Stats } from "./stats";

async function statsPage(): Promise<string> {
  const root = createRootRoute();
  const stats = createRoute({
    getParentRoute: () => root,
    path: "/stats",
    validateSearch: validateProjectSearch,
    component: Stats,
  });
  const router = createRouter({
    routeTree: root.addChildren([stats]),
    history: createMemoryHistory({ initialEntries: ["/stats"] }),
  });
  await router.load();
  return renderToString(<RouterProvider router={router} />);
}

describe("the Statistics page", () => {
  it("draws the run history once, as the last section of the Run success card above the Rewrite rate card", async () => {
    const html = await statsPage();
    const at = (id: string) => html.indexOf(`aria-labelledby="${id}"`);
    const between = html.slice(at("run-history-title"), at("rewrite-rate-title"));
    expect(html.match(/aria-labelledby="run-history-title"/g)).toHaveLength(1);
    expect(at("run-history-title")).toBeGreaterThan(at("run-success-title"));
    expect(between.match(/<\/section>/g)).toHaveLength(2);
  });

  it("stacks the two cards 8px apart and 16px apart beside the filter column", async () => {
    const html = await statsPage();
    const column = html.match(/<div class="([^"]*)"><section aria-labelledby="run-success-title"/);
    expect(column?.[1].split(" ")).toContain("gap-y-2");
    expect(column?.[1].split(" ")).toContain("@[51.25rem]:gap-y-4");
  });

  it("draws the Concurrency card third in the column, under the Rewrite rate card", async () => {
    const html = await statsPage();
    const at = (id: string) => html.indexOf(`aria-labelledby="${id}"`);
    expect(at("concurrency-title")).toBeGreaterThan(at("rewrite-rate-title"));
    expect(at("concurrency-title")).toBeLessThan(html.indexOf("projects"));
  });

  it("gives the Rewrite rate and Concurrency cards the Run success card's phone margin", async () => {
    const html = await statsPage();
    for (const id of ["run-success-title", "rewrite-rate-title", "concurrency-title"]) {
      const card = html.match(new RegExp(`<section aria-labelledby="${id}"[^>]*class="([^"]*)"`));
      expect(card?.[1].split(" ")).toContain("max-sm:-mx-2.5");
    }
  });
});
