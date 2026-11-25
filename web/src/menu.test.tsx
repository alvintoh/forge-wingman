import { describe, expect, it } from "bun:test";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from "@tanstack/react-router";
import { renderToString } from "react-dom/server";
import { MenuScreens } from "./menu";

const LABELS = ["Overview", "Statistics", "Autonomy", "Plans", "Configuration"];

async function menu(rail: boolean): Promise<string> {
  const root = createRootRoute({
    component: () => <MenuScreens rail={rail} actions={5} project={null} />,
  });
  const home = createRoute({ getParentRoute: () => root, path: "/" });
  const router = createRouter({
    routeTree: root.addChildren([home]),
    history: createMemoryHistory({ initialEntries: ["/"] }),
  });
  await router.load();
  return renderToString(<RouterProvider router={router} />);
}

// The link names in the order the menu lists them, each with its destination.
function links(html: string): { name: string; href: string }[] {
  return [...html.matchAll(/<a aria-label="([^"]*)"[^>]*href="([^"]*)"/g)].map((m) => ({
    name: m[1] ?? "",
    href: m[2] ?? "",
  }));
}

describe("the menu", () => {
  it("lists Plans between Autonomy and Configuration in the expanded sidebar and the drawer", async () => {
    const html = await menu(false);
    expect(links(html).map((link) => link.name)).toEqual([
      "Overview, 5 actions",
      "Statistics",
      "Autonomy",
      "Plans",
      "Configuration",
    ]);
    expect(links(html).find((link) => link.name === "Plans")?.href).toBe("/plans");
    for (const label of LABELS) {
      expect(html).toContain(`>${label}</span>`);
    }
  });

  it("lists Plans between Autonomy and Configuration in the icon rail, named by its title", async () => {
    const html = await menu(true);
    expect(links(html).map((link) => link.name)).toEqual([
      "Overview, 5 actions",
      "Statistics",
      "Autonomy",
      "Plans",
      "Configuration",
    ]);
    expect(html).toContain('title="Plans"');
    expect(html).not.toContain(">Plans</span>");
  });

  it("draws Plans' icon as two columns, past the rule that ends the run screens", async () => {
    const html = await menu(false);
    const plans = html.slice(
      html.indexOf('aria-label="Plans"'),
      html.indexOf('aria-label="Configuration"'),
    );
    expect(plans.match(/<path d="M5 4H9[^"]*Z/g)).toHaveLength(1);
    expect(plans).toContain("M15 4H19");
    expect(html.indexOf("border-b")).toBeLessThan(html.indexOf('aria-label="Autonomy"'));
  });
});
