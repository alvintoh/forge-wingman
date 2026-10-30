/** A page the surface serves: the route the router declares and the file that draws it. */
export type Surface = {
  /** The route as web/src/main.tsx declares it, a parameter written `$name`. */
  readonly route: string;
  /** The page file a change to which selects this route. */
  readonly page: string;
};

/**
 * SURFACES lists every page the app routes, in the order the router declares them, each with the
 * page file whose change selects it. The two old settings routes redirect to /config and draw a
 * page of their own nowhere, so they are not captured.
 */
export const SURFACES: readonly Surface[] = [
  { route: "/", page: "web/src/pages/overview.tsx" },
  { route: "/runs/$runId", page: "web/src/pages/run.tsx" },
  { route: "/stats", page: "web/src/pages/stats.tsx" },
  { route: "/config", page: "web/src/pages/config.tsx" },
  { route: "/autonomy", page: "web/src/pages/autonomy.tsx" },
  { route: "/projects", page: "web/src/projects.tsx" },
];

/** A route to capture, and the concrete path when the caller named one (`/runs/abc123`). */
export type Selection = { surface: Surface; path: string | null };

/** unknownRoute is a `--routes` value the router declares no page for. */
export class UnknownRoute extends Error {
  constructor(readonly spec: string) {
    super(`no page is routed at ${spec}; the routes are ${SURFACES.map((s) => s.route).join(", ")}`);
    this.name = "UnknownRoute";
  }
}

/**
 * selectSurfaces is the routes to capture: the named ones when asked for them, else the ones whose
 * page file changed, else every one of them. Naming a concrete path for a parameterised route
 * (`--routes /runs/abc123`) captures that path.
 */
export function selectSurfaces(
  requested: readonly string[] | null,
  changedPages: readonly string[],
): Selection[] {
  if (requested === null) {
    const changed = new Set(changedPages);
    const touched = SURFACES.filter((surface) => changed.has(surface.page));
    return (touched.length > 0 ? touched : SURFACES).map((surface) => ({
      surface,
      path: null,
    }));
  }
  return requested.map((spec) => {
    const exact = SURFACES.find((surface) => surface.route === spec);
    if (exact !== undefined) {
      return { surface: exact, path: null };
    }
    const withParam = SURFACES.find((surface) => {
      const prefix = surface.route.slice(0, surface.route.indexOf("$"));
      return prefix !== "" && spec.startsWith(prefix) && spec.length > prefix.length;
    });
    if (withParam === undefined) {
      throw new UnknownRoute(spec);
    }
    return { surface: withParam, path: spec };
  });
}

/** concretePath is the path to request for a surface, with the run id a parameterised route needs. */
export function concretePath(surface: Surface, runId: string | null): string {
  if (!surface.route.includes("$runId")) {
    return surface.route;
  }
  if (runId === null) {
    throw new Error(
      `${surface.route} needs a run id, and the inbox fixture lists no run to take one from`,
    );
  }
  return surface.route.replace("$runId", encodeURIComponent(runId));
}

/** shotSlug is a route path as a file name: `/` is `index`, and each further segment joins with `-`. */
export function shotSlug(path: string): string {
  const trimmed = path.replace(/^\/+|\/+$/g, "");
  return trimmed === "" ? "index" : trimmed.replaceAll("/", "-");
}
