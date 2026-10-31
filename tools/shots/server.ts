// The fixture server: the built SPA from --dist, and the fixture API from
// --fixtures. It never reaches the network: every /api read is answered from
// the fixtures or recorded as missing, and any other /api request is recorded
// too, so a surface that writes on load fails the run.

import { existsSync, statSync } from "node:fs";
import { extname, join, normalize, sep } from "node:path";
import { FixtureApi } from "./fixtures";

/** The running fixture server and how it reported the requests it saw. */
export type ShotServer = {
  url: string;
  fixtures: FixtureApi;
  // /api requests that were not reads, by "METHOD /path", with their count.
  writes: Map<string, number>;
  stop: () => void;
};

const JSON_HEADERS = { "content-type": "application/json", "cache-control": "no-store" };

/** startServer serves dist and the fixtures on an ephemeral loopback port. */
export function startServer(dist: string, fixturesDir: string): ShotServer {
  const fixtures = new FixtureApi(fixturesDir);
  const writes = new Map<string, number>();

  const server = Bun.serve({
    hostname: "127.0.0.1",
    port: 0,
    fetch(request) {
      const { pathname } = new URL(request.url);
      if (pathname.startsWith("/api/")) {
        if (request.method !== "GET" && request.method !== "HEAD") {
          const key = `${request.method} ${pathname}`;
          writes.set(key, (writes.get(key) ?? 0) + 1);
          return new Response(JSON.stringify({ error: "the fixture API serves reads" }), {
            status: 405,
            headers: JSON_HEADERS,
          });
        }
        const body = fixtures.get(pathname);
        if (body === null) {
          return new Response(JSON.stringify({ error: "no fixture for this route" }), {
            status: 404,
            headers: JSON_HEADERS,
          });
        }
        return new Response(body, { headers: JSON_HEADERS });
      }

      const file = servePath(dist, pathname);
      if (file === null) {
        return new Response("not found", { status: 404 });
      }
      return new Response(Bun.file(file));
    },
  });

  return {
    url: `http://127.0.0.1:${server.port}`,
    fixtures,
    writes,
    stop: () => server.stop(true),
  };
}

/**
 * servePath resolves a request path inside dist: a missing asset is a 404, a
 * path without an extension falls back to index.html so the SPA's own router
 * answers it, and nothing outside dist is ever served.
 */
export function servePath(dist: string, pathname: string): string | null {
  let path = normalize(pathname);
  if (path.includes("\0")) {
    return null;
  }
  let file = join(dist, path);
  // Keep every read inside dist: a `..` segment would otherwise escape it.
  if (file !== dist && !file.startsWith(dist + sep)) {
    return null;
  }
  if (existsSync(file) && statSync(file).isDirectory()) {
    file = join(file, "index.html");
  }
  if (!existsSync(file) || !statSync(file).isFile()) {
    if (extname(file) !== "") {
      return null;
    }
    file = join(dist, "index.html");
  }
  return existsSync(file) ? file : null;
}
