import { describe, expect, test } from "bun:test";
import { parseArgs, UsageError } from "./args";

const ROOT = "/repo";
const CWD = "/repo/web";

describe("the arguments", () => {
  test("default to every route, a shots directory and the built surface in web/", () => {
    expect(parseArgs([], ROOT, CWD)).toEqual({
      routes: null,
      out: "/repo/shots",
      dist: "/repo/web/dist",
      fixtures: "/repo/web/fixtures",
      base: "origin/main",
      help: false,
    });
  });

  test("take the routes to capture, comma-separated", () => {
    expect(parseArgs(["--routes", "/,/stats"], ROOT, CWD).routes).toEqual(["/", "/stats"]);
  });

  test("take a flag's value after an equals sign", () => {
    expect(parseArgs(["--routes=/stats"], ROOT, CWD).routes).toEqual(["/stats"]);
  });

  test("resolve a relative directory against the directory the tool was run in", () => {
    const options = parseArgs(["--out", "artifacts/shots", "--dist", "/tmp/dist"], ROOT, CWD);
    expect(options.out).toBe("/repo/web/artifacts/shots");
    expect(options.dist).toBe("/tmp/dist");
  });

  test("take the ref the changed pages are read against", () => {
    expect(parseArgs(["--base", "origin/trunk"], ROOT, CWD).base).toBe("origin/trunk");
  });

  test("show the help when asked for it", () => {
    expect(parseArgs(["--help"], ROOT, CWD).help).toBe(true);
    expect(parseArgs(["-h"], ROOT, CWD).help).toBe(true);
  });

  test("refuse an argument the tool cannot act on", () => {
    expect(() => parseArgs(["--nope"], ROOT, CWD)).toThrow(UsageError);
    expect(() => parseArgs(["--out"], ROOT, CWD)).toThrow(/--out needs a value/);
    expect(() => parseArgs(["--routes", ""], ROOT, CWD)).toThrow(
      /--routes needs at least one route/,
    );
  });
});
