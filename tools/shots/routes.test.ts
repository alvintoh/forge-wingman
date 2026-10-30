import { describe, expect, test } from "bun:test";
import { join } from "node:path";
import { BANDS, shotName } from "./bands";
import { concretePath, SURFACES, UnknownRoute, selectSurfaces, shotSlug } from "./routes";

const ROOT = join(import.meta.dir, "../..");
const MAIN = join(ROOT, "web/src/main.tsx");

describe("the widths", () => {
  test("are the phone, the tablet and the two desktops, narrowest first", () => {
    expect(BANDS.map((band) => [band.name, band.width, band.height, band.scale])).toEqual([
      ["s", 440, 763, 3],
      ["m", 768, 1024, 1],
      ["l", 1024, 768, 1],
      ["xl", 1280, 800, 1],
    ]);
  });

  test("name each shot <route>-<band>.png", () => {
    expect(BANDS.map((band) => shotName("stats", band))).toEqual([
      "stats-s.png",
      "stats-m.png",
      "stats-l.png",
      "stats-xl.png",
    ]);
  });
});

describe("the routes", () => {
  test("are the ones the router declares", async () => {
    const source = await Bun.file(MAIN).text();
    for (const { route } of SURFACES) {
      expect(source).toContain(`path: "${route}"`);
    }
  });

  test("name a page file that exists", async () => {
    for (const { page } of SURFACES) {
      expect(await Bun.file(join(ROOT, page)).exists()).toBe(true);
    }
  });

  test("are every one of them when no page changed", () => {
    expect(selectSurfaces(null, []).map(({ surface }) => surface.route)).toEqual(
      SURFACES.map(({ route }) => route),
    );
  });

  test("are only the routes whose page changed", () => {
    const picked = selectSurfaces(null, [
      "web/src/pages/stats.tsx",
      "web/src/pages/config.tsx",
      "web/src/inbox/row.tsx",
    ]);
    expect(picked.map(({ surface }) => surface.route)).toEqual(["/stats", "/config"]);
  });

  test("are the ones named, in the order they are named", () => {
    expect(
      selectSurfaces(["/stats", "/"], []).map(({ surface }) => surface.route),
    ).toEqual(["/stats", "/"]);
  });

  test("take a concrete path for a parameterised route", () => {
    expect(selectSurfaces(["/runs/run_01jfv3k7"], [])).toEqual([
      { surface: SURFACES[1]!, path: "/runs/run_01jfv3k7" },
    ]);
  });

  test("refuse a route no page is drawn at", () => {
    expect(() => selectSurfaces(["/nope"], [])).toThrow(UnknownRoute);
  });
});

describe("the shot names", () => {
  test("call the root index and join the rest with hyphens", () => {
    expect(shotSlug("/")).toBe("index");
    expect(shotSlug("/stats")).toBe("stats");
    expect(shotSlug("/runs/run_01jfv3k7")).toBe("runs-run_01jfv3k7");
  });

  test("need the run id a parameterised route is captured at", () => {
    expect(concretePath(SURFACES[1]!, "run_01jfv3k7")).toBe("/runs/run_01jfv3k7");
    expect(concretePath(SURFACES[0]!, null)).toBe("/");
    expect(() => concretePath(SURFACES[1]!, null)).toThrow(/needs a run id/);
  });
});
