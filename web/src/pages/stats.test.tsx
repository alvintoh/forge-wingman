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
});
