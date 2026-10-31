import { afterAll, beforeEach, describe, expect, test } from "bun:test";
import { CDP } from "./cdp";

// A WebSocket server standing in for the browser: each test sets the handler
// that answers the client's commands.
let handler: (ws: { send(data: string): void }, message: string) => void = () => {};

const server = Bun.serve({
  hostname: "127.0.0.1",
  port: 0,
  fetch(request, upgrade) {
    if (upgrade.upgrade(request)) {
      return;
    }
    return new Response("upgrade failed", { status: 400 });
  },
  websocket: {
    message(ws, message) {
      handler(ws, String(message));
    },
  },
});

const cdp = await CDP.connect(`ws://127.0.0.1:${server.port}`, { commandTimeoutMs: 100 });

afterAll(() => {
  cdp.close();
  server.stop(true);
});

beforeEach(() => {
  handler = () => {};
});

describe("CDP", () => {
  test("resolves a command's answer by id", async () => {
    handler = (ws, message) => {
      const command = JSON.parse(message);
      ws.send(JSON.stringify({ id: command.id, result: { ok: true } }));
    };
    expect(await cdp.send("Echo")).toEqual({ ok: true });
  });

  test("rejects a command the browser refuses", async () => {
    handler = (ws, message) => {
      const command = JSON.parse(message);
      ws.send(JSON.stringify({ id: command.id, error: { message: "no such target" } }));
    };
    await expect(cdp.send("Attach")).rejects.toThrow("no such target");
  });

  test("fails a command that is never answered", async () => {
    await expect(cdp.send("Slow")).rejects.toThrow("Slow did not answer within 100ms");
  });

  test("resolves waitFor on the event it listens for", async () => {
    const waiting = cdp.waitFor("Test.event", null, 500);
    handler = (ws, message) => {
      const command = JSON.parse(message);
      ws.send(JSON.stringify({ id: command.id, result: {} }));
      ws.send(JSON.stringify({ method: "Test.event", params: { value: 7 } }));
    };
    await cdp.send("Trigger");
    expect(await waiting).toEqual({ value: 7 });
  });

  test("fails waitFor when the event never comes", async () => {
    await expect(cdp.waitFor("Never.event", null, 50)).rejects.toThrow(
      "the page never sent Never.event within 50ms",
    );
  });

  test("fails to connect to a closed port", async () => {
    const dead = Bun.serve({ port: 0, fetch: () => new Response("closed") });
    const closed = `ws://127.0.0.1:${dead.port}`;
    dead.stop(true);
    await expect(CDP.connect(closed, { connectTimeoutMs: 500 })).rejects.toThrow(
      "cannot connect to the browser",
    );
  });
});
