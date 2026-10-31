import { afterAll, describe, expect, test } from "bun:test";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  captureShot,
  type CommandChannel,
  FIXED_TIME,
  preCaptureRefusal,
  pngSize,
  Refused,
  sizeError,
  VIEWPORTS,
  type Viewport,
} from "./capture";
import type { Page } from "./chromium";

const cleanup: string[] = [];

afterAll(() => {
  for (const dir of cleanup) {
    rmSync(dir, { recursive: true, force: true });
  }
});

describe("VIEWPORTS", () => {
  test("are the four review widths, the phone at three device pixels", () => {
    expect(VIEWPORTS.map((viewport) => [viewport.key, viewport.width, viewport.height])).toEqual([
      ["s", 440, 763],
      ["m", 768, 1024],
      ["l", 1024, 768],
      ["xl", 1280, 800],
    ]);
    expect(VIEWPORTS[0]?.deviceScaleFactor).toBe(3);
    expect(VIEWPORTS[0]?.mobile).toBe(true);
    expect(VIEWPORTS.slice(1).every((viewport) => !viewport.mobile)).toBe(true);
  });

  test("pin one clock for the run", () => {
    expect(new Date(FIXED_TIME).toISOString()).toBe("2026-03-01T12:00:00.000Z");
  });
});

describe("preCaptureRefusal", () => {
  test("refuses a control hovered at every edge", () => {
    const refusal = preCaptureRefusal({ hovered: ["button: show"], overflow: null, late: null });
    expect(refusal).toBeInstanceOf(Refused);
    expect(refusal?.message).toBe("a control is hovered at every edge: button: show");
  });

  test("refuses a page that scrolls sideways", () => {
    const refusal = preCaptureRefusal({ hovered: [], overflow: 12, late: null });
    expect(refusal).toBeInstanceOf(Refused);
    expect(refusal?.message).toBe("the page scrolls sideways: 12px wider than the viewport");
  });

  test("refuses a page that changed before the shot", () => {
    const refusal = preCaptureRefusal({
      hovered: [],
      overflow: null,
      late: "an element is aria-busy: section",
    });
    expect(refusal).toBeInstanceOf(Refused);
    expect(refusal?.message).toBe(
      "the page changed before the shot: an element is aria-busy: section",
    );
  });

  test("allows a page with none of them", () => {
    expect(preCaptureRefusal({ hovered: [], overflow: null, late: null })).toBeNull();
  });

  test("names the hovered control before the sideways scroll", () => {
    const refusal = preCaptureRefusal({ hovered: ["button"], overflow: 3, late: null });
    expect(refusal?.message).toBe("a control is hovered at every edge: button");
  });
});

describe("sizeError", () => {
  const phone = VIEWPORTS[0]!;

  test("allows a shot at the viewport's device size", () => {
    expect(sizeError(phone, { width: 1320, height: 2289 })).toBeNull();
  });

  test("names both sizes when the shot is the wrong size", () => {
    expect(sizeError(phone, { width: 440, height: 763 })).toBe(
      "the browser shot 440x763 for s; wanted 1320x2289",
    );
  });
});

describe("captureShot", () => {
  const phone = VIEWPORTS[0]!;

  function png(width: number, height: number): Uint8Array {
    const bytes = new Uint8Array(24);
    bytes.set([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a], 0);
    bytes.set([0x49, 0x48, 0x44, 0x52], 12);
    const view = new DataView(bytes.buffer);
    view.setUint32(16, width);
    view.setUint32(20, height);
    return bytes;
  }

  type FakeOptions = {
    // The READY expression's answer: null is settled, a string is the reason not.
    ready?: string | null;
    // The answer to every READY evaluation after the first, when it differs: the
    // page's state at the moment of capture.
    lateReady?: string | null;
    readyThrows?: string;
    hovered?: string[];
    overflow?: number | null;
    shot?: Uint8Array;
  };

  // expressionOf reads a Runtime.evaluate command's expression, or empty when
  // the command carries none.
  function expressionOf(params: object): string {
    if ("expression" in params && typeof params.expression === "string") {
      return params.expression;
    }
    return "";
  }

  // fakeChannel answers the CDP commands a shot makes, so the capture flow can
  // be driven without a browser.
  function fakeChannel(options: FakeOptions): CommandChannel & { calls: string[] } {
    const calls: string[] = [];
    let readyCalls = 0;
    return {
      calls,
      async send<T>(method: string, params: object = {}): Promise<T> {
        calls.push(method);
        if (method === "Runtime.evaluate") {
          const expression = expressionOf(params);
          if (expression.includes("aria-busy")) {
            if (options.readyThrows !== undefined) {
              return { exceptionDetails: { text: options.readyThrows } } as T;
            }
            const answer =
              readyCalls++ === 0 || options.lateReady === undefined
                ? options.ready
                : options.lateReady;
            return { result: { value: answer ?? null } } as T;
          }
          if (expression.includes(":hover")) {
            return { result: { value: options.hovered ?? [] } } as T;
          }
          if (expression.includes("document.documentElement")) {
            return { result: { value: options.overflow ?? null } } as T;
          }
          return { result: { value: true } } as T;
        }
        if (method === "Page.captureScreenshot") {
          const shot = options.shot ?? png(1320, 2289);
          return { data: Buffer.from(shot).toString("base64") } as T;
        }
        return {} as T;
      },
      async waitFor(): Promise<Record<string, unknown>> {
        return {};
      },
    };
  }

  function drivenPage(): Page {
    return { sessionId: "s", pending: new Set(), errors: [] };
  }

  function outDir(): string {
    const dir = mkdtempSync(join(tmpdir(), "shots-capture-"));
    cleanup.push(dir);
    return dir;
  }

  async function refusalOf(channel: CommandChannel, viewport: Viewport): Promise<Error> {
    return captureShot({
      cdp: channel,
      page: drivenPage(),
      url: "http://127.0.0.1:9/",
      viewport,
      out: join(outDir(), "refused.png"),
    }).then(
      () => new Error("the shot was taken"),
      (err: Error) => err,
    );
  }

  test("writes a shot at the viewport's device size", async () => {
    const bytes = png(1320, 2289);
    const channel = fakeChannel({ shot: bytes });
    const dir = outDir();
    const out = join(dir, "s.png");
    const size = await captureShot({
      cdp: channel,
      page: drivenPage(),
      url: "http://127.0.0.1:9/",
      viewport: phone,
      out,
    });
    expect(size).toEqual({ width: 1320, height: 2289 });
    expect(readFileSync(out).equals(bytes)).toBe(true);
    expect(channel.calls).toContain("Page.captureScreenshot");
  });

  test("refuses a page with a hovered control and does not capture it", async () => {
    const channel = fakeChannel({ hovered: ["button: show"] });
    const refusal = await refusalOf(channel, phone);
    expect(refusal).toBeInstanceOf(Refused);
    expect(refusal.message).toBe("a control is hovered at every edge: button: show");
    expect(channel.calls).not.toContain("Page.captureScreenshot");
  });

  test("refuses a page that scrolls sideways and does not capture it", async () => {
    const channel = fakeChannel({ overflow: 12 });
    const refusal = await refusalOf(channel, phone);
    expect(refusal).toBeInstanceOf(Refused);
    expect(refusal.message).toBe("the page scrolls sideways: 12px wider than the viewport");
    expect(channel.calls).not.toContain("Page.captureScreenshot");
  });

  test("refuses a page that stops being settled after the gate and does not capture it", async () => {
    const channel = fakeChannel({ ready: null, lateReady: "an element is aria-busy: section" });
    const refusal = await refusalOf(channel, phone);
    expect(refusal).toBeInstanceOf(Refused);
    expect(refusal.message).toBe(
      "the page changed before the shot: an element is aria-busy: section",
    );
    expect(channel.calls).not.toContain("Page.captureScreenshot");
  });

  test("throws when the page never settles", async () => {
    const realNow = Date.now;
    let clock = 0;
    Date.now = () => (clock += 60_000);
    try {
      const refusal = await refusalOf(
        fakeChannel({ ready: "an element is aria-busy: body" }),
        phone,
      );
      expect(refusal.message).toContain("the page did not settle: an element is aria-busy: body");
    } finally {
      Date.now = realNow;
    }
  });

  test("throws when the page itself throws", async () => {
    const refusal = await refusalOf(fakeChannel({ readyThrows: "boom" }), phone);
    expect(refusal.message).toBe("the page threw: boom");
  });

  test("throws when the browser shot the wrong size", async () => {
    const refusal = await refusalOf(fakeChannel({ shot: png(440, 763) }), phone);
    expect(refusal.message).toBe("the browser shot 440x763 for s; wanted 1320x2289");
  });
});

describe("pngSize", () => {
  function png(width: number, height: number): Uint8Array {
    const bytes = new Uint8Array(24);
    bytes.set([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a], 0);
    bytes.set([0x49, 0x48, 0x44, 0x52], 12);
    const view = new DataView(bytes.buffer);
    view.setUint32(16, width);
    view.setUint32(20, height);
    return bytes;
  }

  test("reads a shot's pixel size", () => {
    expect(pngSize(png(1320, 2289))).toEqual({ width: 1320, height: 2289 });
  });

  test("rejects anything that is not a PNG", () => {
    expect(() => pngSize(new Uint8Array(24))).toThrow("PNG");
  });
});
