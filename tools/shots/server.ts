import { extname, join, resolve, sep } from "node:path";

/** A request the app made to the api, and what it was answered with. */
export type ApiHit = { route: string; status: number };

export type ShotServer = {
  /** The origin the surface is served from. */
  url: string;
  /** Every api request the app has made, in the order it made them. */
  hits: ApiHit[];
  /** The routes the app read that no fixture answers, each once, in the order it was read. */
  missing: () => string[];
  stop: () => void;
};

export type ServerOptions = {
  /** The built surface: its index.html, its assets and everything public/. */
  dist: string;
  /** One JSON file per api route, named by the path under the api. */
  fixtures: string;
};

const API = "/api/";
const JSON_TYPE = "application/json; charset=utf-8";

function notFound(message: string): Response {
  return new Response(`${message}\n`, { status: 404 });
}

/**
 * startServer serves a built surface with a fixture api: every api route answers the JSON file of
 * its path, so nothing the app reads is live, and every other path is the built file or the SPA's
 * index document. A route with no fixture is recorded and answered 404 rather than left to hang.
 */
export function startServer({ dist, fixtures }: ServerOptions): ShotServer {
  const hits: ApiHit[] = [];
  const distRoot = resolve(dist);
  const fixturesRoot = resolve(fixtures);

  async function api(path: string): Promise<Response> {
    const file = Bun.file(join(fixturesRoot, `${path.slice(API.length)}.json`));
    if (!(await file.exists())) {
      hits.push({ route: path, status: 404 });
      return notFound(`no fixture for ${path}`);
    }
    hits.push({ route: path, status: 200 });
    return new Response(file, {
      headers: { "content-type": JSON_TYPE, "cache-control": "no-store" },
    });
  }

  async function asset(path: string): Promise<Response> {
    const relative = path.replace(/^\/+/, "");
    const candidate = resolve(distRoot, relative);
    if (candidate === distRoot || candidate.startsWith(`${distRoot}${sep}`)) {
      const file = Bun.file(candidate);
      if (await file.exists()) {
        return new Response(file, {
          headers: { "content-type": file.type || "application/octet-stream" },
        });
      }
    }
    // A path with an extension names a file the build should have written; anything else is a
    // route, which the SPA answers itself from its index document.
    if (relative !== "" && extname(relative) !== "") {
      return notFound(`no build file at ${path}`);
    }
    const index = Bun.file(join(distRoot, "index.html"));
    if (!(await index.exists())) {
      return new Response(`the build has no index.html\n`, { status: 500 });
    }
    return new Response(index, { headers: { "content-type": "text/html; charset=utf-8" } });
  }

  const server = Bun.serve({
    hostname: "127.0.0.1",
    port: 0,
    async fetch(request) {
      let path: string;
      try {
        path = decodeURIComponent(new URL(request.url).pathname);
      } catch {
        return notFound("the path is not valid utf-8");
      }
      // Neither the fixture lookup nor the build files may be reached with a path that climbs out
      // of its directory, however it is encoded.
      if (path.includes("\0") || path.includes("\\") || path.split("/").includes("..")) {
        return notFound(`no file at ${path}`);
      }
      return path.startsWith(API) ? api(path) : asset(path);
    },
  });

  return {
    url: `http://127.0.0.1:${server.port}`,
    hits,
    missing() {
      return [
        ...new Set(hits.filter((hit) => hit.status === 404).map((hit) => hit.route)),
      ];
    },
    stop() {
      server.stop(true);
    },
  };
}
