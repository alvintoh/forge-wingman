import { afterEach, describe, expect, test } from "bun:test";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { Page } from "playwright";
import { BANDS } from "./bands";
import {
  contextOptions,
  FIXED_TIME,
  launchFailure,
  SETTLE_MS,
  shoot,
} from "./shoot";

type Behaviour = {
  fonts?: boolean;
  busy?: boolean;
  hovered?: string[];
  overflow?: number;
  consoleErrors?: string[];
  pageError?: string;
};

/** fakePage is a page whose every observation is scripted, so one gate can fail alone. */
function fakePage(behaviour: Behaviour = {}) {
  const calls: string[] = [];
  const listeners: Record<string, ((argument: never) => void)[]> = {};
  const shots: { path: string; type: string; fullPage: boolean; animations: string }[] = [];
  let fixedAt: Date | undefined;
  let gotoOptions: { waitUntil?: string; timeout?: number } | undefined;

  function emit(event: string, argument: unknown): void {
    for (const handler of listeners[event] ?? []) {
      (handler as (value: unknown) => void)(argument);
    }
  }

  const page = {
    on(event: string, handler: (argument: never) => void) {
      (listeners[event] ??= []).push(handler);
    },
    clock: {
      async setFixedTime(time: Date) {
        fixedAt = time;
        calls.push("clock");
      },
    },
    async goto(_url: string, options: { waitUntil?: string; timeout?: number }) {
      calls.push("goto");
      gotoOptions = options;
      for (const text of behaviour.consoleErrors ?? []) {
        emit("console", { type: () => "error", text: () => text });
      }
      if (behaviour.pageError !== undefined) {
        emit("pageerror", new Error(behaviour.pageError));
      }
    },
    async waitForLoadState() {
      calls.push("networkidle");
    },
    async addStyleTag() {
      calls.push("style");
    },
    async evaluate(fn: (argument?: never) => unknown) {
      const source = fn.toString();
      if (source.includes("document.fonts")) {
        calls.push("fonts");
        return behaviour.fonts ?? true;
      }
      if (source.includes("scrollWidth")) {
        calls.push("overflow");
        return behaviour.overflow ?? 0;
      }
      calls.push("hover");
      return behaviour.hovered ?? [];
    },
    async waitForFunction() {
      calls.push("busy");
      if (behaviour.busy === true) {
        throw new Error(`Timeout ${SETTLE_MS}ms exceeded`);
      }
    },
    mouse: {
      async move(x: number, y: number) {
        calls.push(`move:${x},${y}`);
      },
    },
    async screenshot(options: {
      path: string;
      type: string;
      fullPage: boolean;
      animations: string;
    }) {
      calls.push("screenshot");
      shots.push(options);
    },
  };

  return {
    page: page as unknown as Page,
    calls,
    shots,
    fixedAt: () => fixedAt,
    gotoOptions: () => gotoOptions,
  };
}

const work = await mkdtemp(join(tmpdir(), "shots-shoot-"));

afterEach(async () => {
  for (const name of ["stats-s.png", "stats-xl.png"]) {
    await rm(join(work, name), { force: true });
  }
});

const band = BANDS[0]!;

describe("a shot", () => {
  test("is written at the band's own name, as the full viewport", async () => {
    const fake = fakePage();
    const result = await shoot({
      page: fake.page,
      url: "http://shot.test/stats",
      outDir: work,
      slug: "stats",
      band,
    });
    expect(result).toEqual({ ok: true, path: join(work, "stats-s.png") });
    expect(fake.shots).toEqual([
      {
        path: join(work, "stats-s.png"),
        type: "png",
        fullPage: false,
        animations: "disabled",
      },
    ]);
  });

  test("is loaded at the band's width on a fixed clock", async () => {
    const fake = fakePage();
    await shoot({
      page: fake.page,
      url: "http://shot.test/stats",
      outDir: work,
      slug: "stats",
      band: BANDS[3]!,
    });
    expect(fake.fixedAt()).toEqual(FIXED_TIME);
    expect(fake.gotoOptions()).toEqual({ waitUntil: "load", timeout: SETTLE_MS });
  });

  test("is refused while the fonts have not loaded", async () => {
    const fake = fakePage({ fonts: false });
    const result = await shoot({
      page: fake.page,
      url: "http://shot.test/stats",
      outDir: work,
      slug: "stats",
      band,
    });
    expect(result).toEqual({ ok: false, reason: "the fonts did not finish loading" });
    expect(fake.shots).toEqual([]);
  });

  test("is refused while anything still reports aria-busy", async () => {
    const fake = fakePage({ busy: true });
    const result = await shoot({
      page: fake.page,
      url: "http://shot.test/stats",
      outDir: work,
      slug: "stats",
      band,
    });
    expect(result.ok).toBe(false);
    expect(result.ok === false && result.reason).toContain("aria-busy");
    expect(fake.shots).toEqual([]);
  });

  test("is refused when the app logged a read that failed", async () => {
    const fake = fakePage({ consoleErrors: ["inboxNotRead Error: the inbox endpoint answered 500"] });
    const result = await shoot({
      page: fake.page,
      url: "http://shot.test/stats",
      outDir: work,
      slug: "stats",
      band,
    });
    expect(result.ok).toBe(false);
    expect(result.ok === false && result.reason).toContain("the app could not read its fixtures");
    expect(result.ok === false && result.reason).toContain("inboxNotRead");
    expect(fake.shots).toEqual([]);
  });

  test("is refused when the page threw", async () => {
    const fake = fakePage({ pageError: "boom" });
    const result = await shoot({
      page: fake.page,
      url: "http://shot.test/stats",
      outDir: work,
      slug: "stats",
      band,
    });
    expect(result.ok === false && result.reason).toContain("the page threw: boom");
    expect(fake.shots).toEqual([]);
  });

  test("is refused when a control answers the pointer", async () => {
    const fake = fakePage({ hovered: ["button"] });
    const result = await shoot({
      page: fake.page,
      url: "http://shot.test/stats",
      outDir: work,
      slug: "stats",
      band,
    });
    expect(result.ok === false && result.reason).toContain("a control is under the pointer");
    expect(result.ok === false && result.reason).toContain("(button)");
    expect(fake.shots).toEqual([]);
  });

  test("is refused when the page scrolls sideways", async () => {
    const fake = fakePage({ overflow: 12 });
    const result = await shoot({
      page: fake.page,
      url: "http://shot.test/stats",
      outDir: work,
      slug: "stats",
      band,
    });
    expect(result.ok === false && result.reason).toContain("scrolls sideways: 12px");
    expect(fake.shots).toEqual([]);
  });

  test("is taken with the pointer parked, so it is not the pointer's own hover", async () => {
    const fake = fakePage();
    await shoot({
      page: fake.page,
      url: "http://shot.test/stats",
      outDir: work,
      slug: "stats",
      band,
    });
    expect(fake.calls.indexOf("move:0,0")).toBeLessThan(fake.calls.indexOf("hover"));
  });

  test("leaves a console error that is not a failed read alone", async () => {
    const fake = fakePage({ consoleErrors: ["signOutNotCleared Error: no redirect"] });
    const result = await shoot({
      page: fake.page,
      url: "http://shot.test/stats",
      outDir: work,
      slug: "stats",
      band,
    });
    expect(result.ok).toBe(true);
  });

  test("removes the file an earlier run wrote when it is refused", async () => {
    const stale = join(work, "stats-xl.png");
    await writeFile(stale, "an earlier run's shot");
    const fake = fakePage({ overflow: 3 });
    const result = await shoot({
      page: fake.page,
      url: "http://shot.test/stats",
      outDir: work,
      slug: "stats",
      band: BANDS[3]!,
    });
    expect(result.ok).toBe(false);
    expect(await Bun.file(stale).exists()).toBe(false);
  });
});

describe("the context a shot is taken in", () => {
  test("is the band's width and pixel ratio, with everything else pinned", () => {
    expect(contextOptions(BANDS[0]!)).toEqual({
      viewport: { width: 440, height: 763 },
      deviceScaleFactor: 3,
      colorScheme: "light",
      reducedMotion: "reduce",
      locale: "en-GB",
      timezoneId: "UTC",
    });
    expect(contextOptions(BANDS[3]!).deviceScaleFactor).toBe(1);
    expect(contextOptions(BANDS[3]!).viewport).toEqual({ width: 1280, height: 800 });
  });
});

describe("a browser that will not start", () => {
  test("is reported with the two installs it needs", () => {
    const err = launchFailure(new Error("Executable doesn't exist"));
    expect(err.message).toContain("Executable doesn't exist");
    expect(err.message).toContain("bun install");
    expect(err.message).toContain("bunx playwright install chromium");
  });
});
