import { describe, expect, test } from "bun:test";
import { parseArgs, UsageError } from "./cli";

describe("parseArgs", () => {
  test("reads the flags in either form and defaults the rest", () => {
    expect(
      parseArgs(["--out", "shots", "--routes=/stats,/config", "--base", "origin/main"]),
    ).toEqual({
      dist: null,
      fixtures: null,
      out: "shots",
      routes: ["/stats", "/config"],
      base: "origin/main",
      chromium: null,
      ticket: null,
      help: false,
    });
  });

  test("reads the ticket file to check", () => {
    expect(parseArgs(["--out", "x", "--ticket", "run/ticket.json"]).ticket).toBe("run/ticket.json");
    expect(parseArgs(["--out", "x", "--ticket=run/ticket.json"]).ticket).toBe("run/ticket.json");
  });

  test("rejects --ticket with no value", () => {
    expect(() => parseArgs(["--out", "x", "--ticket"])).toThrow("--ticket needs a value");
  });

  test("requires --out", () => {
    expect(() => parseArgs([])).toThrow(UsageError);
    expect(() => parseArgs([])).toThrow("--out is required");
  });

  test("rejects an unknown flag instead of ignoring it", () => {
    expect(() => parseArgs(["--out", "x", "--shots", "all"])).toThrow("unknown flag --shots");
  });

  test("rejects a flag with no value", () => {
    expect(() => parseArgs(["--out"])).toThrow("--out needs a value");
  });

  test("rejects an empty route list", () => {
    expect(() => parseArgs(["--out", "x", "--routes", " , "])).toThrow(
      "--routes needs at least one route",
    );
  });

  test("help needs no --out", () => {
    expect(parseArgs(["--help"]).help).toBe(true);
  });
});
