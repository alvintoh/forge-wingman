import { afterAll, describe, expect, test } from "bun:test";
import { mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { findChromium } from "./chromium";
import { run } from "./main";
import { quietly } from "./quietly";

// run() end to end over a synthetic build, so the exit code and the failure
// lines it reports are proven against a real browser. Skipped when the machine
// has no Chromium.
const executable = findChromium(null);
const cleanup: string[] = [];

afterAll(() => {
  for (const dir of cleanup) {
    rmSync(dir, { recursive: true, force: true });
  }
});

function tempDir(): string {
  const dir = mkdtempSync(join(tmpdir(), "shots-main-e2e-"));
  cleanup.push(dir);
  return dir;
}

function buildOf(body: string): string {
  const dir = tempDir();
  writeFileSync(
    join(dir, "index.html"),
    `<!doctype html><meta name="viewport" content="width=device-width, initial-scale=1.0"><body>${body}</body>`,
  );
  return dir;
}

// ticketOf writes a ticket file whose description holds the given criteria.
function ticketOf(criteria: string): string {
  const path = join(tempDir(), "ticket.json");
  writeFileSync(
    path,
    JSON.stringify({ id: "T-1", body: `## Acceptance criteria\n\n${criteria}\n` }),
  );
  return path;
}

async function shoot(
  body: string,
  ticket?: string,
): Promise<{ code: number; lines: string[]; out: string }> {
  const out = tempDir();
  const result = await quietly(() =>
    run([
      "--out",
      out,
      "--dist",
      buildOf(body),
      "--fixtures",
      tempDir(),
      "--routes",
      "/",
      "--chromium",
      executable!,
      ...(ticket === undefined ? [] : ["--ticket", ticket]),
    ]),
  );
  return { ...result, out };
}

describe.skipIf(executable === null)("run end to end", () => {
  test("writes every shot and exits 0 for a page with nothing wrong", async () => {
    const { code, lines, out } = await shoot("<p>fine</p>");
    expect(code).toBe(0);
    expect(readdirSync(out, { recursive: true }).sort()).toEqual([
      "l",
      "l/index.png",
      "m",
      "m/index.png",
      "s",
      "s/index.png",
      "xl",
      "xl/index.png",
    ]);
    expect(lines.filter((line) => line.startsWith("shotsDone count=4 "))).toHaveLength(1);
  }, 60_000);

  test("exits 1 and names each kind of failure", async () => {
    const { code, lines } = await shoot(`
      <div style="width: 2000px; height: 20px"></div>
      <script>
        fetch("/api/absent").then((answer) => {
          if (!answer.ok) console.log("answered", answer.status);
        });
        fetch("/api/save", { method: "POST", body: "{}" }).then(() => {});
        console.error("pageBroke");
      </script>`);
    expect(code).toBe(1);
    expect(lines).toContain("shotsFailed reason=missing-fixture route=/api/absent count=5");
    expect(lines).toContain('shotsFailed reason=unexpected-write request="POST /api/save" count=5');
    expect(lines.some((line) => line.startsWith("shotsFailed reason=refused shot=root-s "))).toBe(
      true,
    );
    expect(lines.some((line) => line.startsWith("shotsFailed reason=page-error route=/ "))).toBe(
      true,
    );
    expect(lines.some((line) => line.startsWith("shotsDone"))).toBe(false);
  }, 60_000);

  test("fails a card wider than its box at the phone widths and still writes every shot", async () => {
    const { code, lines, out } = await shoot(
      '<div class="card-frame" style="width: 300px; overflow: auto"><div style="width: 2000px">wide</div></div>',
    );
    expect(code).toBe(1);
    const overflows = lines.filter((line) => line.startsWith("shotsFailed reason=overflow "));
    expect(overflows).toEqual([
      'shotsFailed reason=overflow state=/@s element="div.card-frame" scrollWidth=2000 clientWidth=300',
      'shotsFailed reason=overflow state=/@xs element="div.card-frame" scrollWidth=2000 clientWidth=300',
    ]);
    expect(
      readdirSync(out, { recursive: true }).filter((name) => String(name).endsWith(".png")),
    ).toHaveLength(4);
  }, 60_000);

  test("names a card wider than the viewport, with both widths, beside the refused shot", async () => {
    const { code, lines } = await shoot('<div class="card-frame" style="width: 2000px">wide</div>');
    expect(code).toBe(1);
    expect(lines.some((line) => line.startsWith("shotsFailed reason=refused shot=root-s "))).toBe(
      true,
    );
    expect(lines).toContain(
      'shotsFailed reason=overflow state=/@s element="div.card-frame" scrollWidth=2000 clientWidth=440',
    );
    expect(lines).toContain(
      'shotsFailed reason=overflow state=/@xs element="div.card-frame" scrollWidth=2000 clientWidth=320',
    );
  }, 60_000);

  test("fails an auto-layout table wider than the box that clips it", async () => {
    const { code, lines } = await shoot(
      `<div style="width: 300px; overflow: hidden"><table><tr><td style="white-space: nowrap">${"x".repeat(70)}</td></tr></table></div>`,
    );
    expect(code).toBe(1);
    const overflows = lines.filter((line) => line.startsWith("shotsFailed reason=overflow "));
    expect(overflows).toHaveLength(2);
    expect(overflows[0]).toMatch(
      /^shotsFailed reason=overflow state=\/@s element="table" scrollWidth=\d+ clientWidth=300$/,
    );
  }, 60_000);

  test("says so when the ticket's criteria quote nothing to check", async () => {
    const { code, lines } = await shoot("<p>fine</p>", ticketOf("1. looks right"));
    expect(code).toBe(0);
    expect(lines).toContain("shotsUnchecked reason=no-criteria");
  }, 60_000);

  test("passes a card and a table that exactly fit their boxes", async () => {
    const { code, lines } = await shoot(
      '<div class="card-frame" style="width: 300px">fits</div><table style="width: 200px"><tr><td>fits</td></tr></table>',
    );
    expect(code).toBe(0);
    expect(lines.some((line) => line.includes("reason=overflow"))).toBe(false);
  }, 60_000);

  test("fails a table wider than its box", async () => {
    const { code, lines } = await shoot(
      '<div style="width: 200px; overflow: hidden"><table style="width: 200px; table-layout: fixed"><tr><td><div style="width: 900px">x</div></td></tr></table></div>',
    );
    expect(code).toBe(1);
    expect(
      lines.some((line) =>
        line.startsWith('shotsFailed reason=overflow state=/@s element="table" '),
      ),
    ).toBe(true);
  }, 60_000);

  test("passes strings the page shows, including text inside an svg", async () => {
    const ticket = ticketOf('1. shows "chart label" and “Plain text.”');
    const { code, out } = await shoot(
      '<p>plain TEXT</p><svg width="100" height="20"><text x="0" y="15">chart label</text></svg>',
      ticket,
    );
    expect(code).toBe(0);
    expect(JSON.parse(readFileSync(join(out, "checks.json"), "utf8"))).toEqual({
      checked: 2,
      missing: [],
      unchecked: [],
    });
  }, 60_000);

  test("fails a quoted string the page never shows, names its state and still writes every shot", async () => {
    const ticket = ticketOf('1. shows "Absent words"\n2. no longer shows "Plain"');
    const { code, lines, out } = await shoot("<p>Plain</p>", ticket);
    expect(code).toBe(1);
    expect(lines).toContain('shotsFailed reason=missing-string state=*@* string="Absent words"');
    expect(lines).toContain('shotsUnchecked reason=negated item=2 string="Plain"');
    const files = readdirSync(out, { recursive: true });
    expect(files.filter((name) => String(name).endsWith(".png"))).toHaveLength(4);
    expect(JSON.parse(readFileSync(join(out, "checks.json"), "utf8")).missing).toEqual([
      { state: "*@*", string: "Absent words" },
    ]);
  }, 60_000);
});
