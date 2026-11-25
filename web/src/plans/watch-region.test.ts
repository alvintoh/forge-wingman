import { describe, expect, it } from "bun:test";
import { measureOf, seenFrom, watchRegion } from "./watch-region";

type Listener = () => void;

// An element that records the listeners it is given and the order they came in.
function fakeElement() {
  const added: [string, Listener, unknown][] = [];
  const removed: [string, Listener][] = [];
  return {
    added,
    removed,
    // The members watchRegion needs of an element; the rest of an HTMLElement it never touches.
    addEventListener(type: string, listener: Listener, options?: unknown) {
      added.push([type, listener, options]);
    },
    removeEventListener(type: string, listener: Listener) {
      removed.push([type, listener]);
    },
  };
}

// A resize observer that records what it is given.
function fakeObserver() {
  const log = { made: [] as Listener[], observed: [] as unknown[], disconnected: 0 };
  class Observer {
    constructor(callback: Listener) {
      log.made.push(callback);
    }
    observe(target: unknown) {
      log.observed.push(target);
    }
    disconnect() {
      log.disconnected += 1;
    }
  }
  return { log, Observer };
}

describe("watchRegion", () => {
  it("reads once on starting, before anything fires", () => {
    const element = fakeElement();
    const { Observer } = fakeObserver();
    let reads = 0;
    watchRegion(element, Observer, () => (reads += 1));
    expect(reads).toBe(1);
  });

  it("reads again on a scroll, which it hears passively, and on a resize", () => {
    const element = fakeElement();
    const { log, Observer } = fakeObserver();
    let reads = 0;
    watchRegion(element, Observer, () => (reads += 1));
    const [scroll] = element.added;
    expect([scroll?.[0], scroll?.[2]]).toEqual(["scroll", { passive: true }]);
    scroll?.[1]();
    expect(reads).toBe(2);
    log.made[0]?.();
    expect(reads).toBe(3);
  });

  it("observes the element's size", () => {
    const element = fakeElement();
    const { log, Observer } = fakeObserver();
    watchRegion(element, Observer, () => {});
    expect(log.observed).toEqual([element]);
  });

  it("stops by removing the very listener it added, and disconnecting the observer", () => {
    const element = fakeElement();
    const { log, Observer } = fakeObserver();
    const stop = watchRegion(element, Observer, () => {});
    expect(element.removed).toEqual([]);
    expect(log.disconnected).toBe(0);
    stop();
    expect(element.removed).toEqual([["scroll", element.added[0]?.[1]]] as [string, Listener][]);
    expect(log.disconnected).toBe(1);
  });
});

describe("measureOf", () => {
  it("reads the scroll position and width of the region and the width of the label cell", () => {
    expect(measureOf({ scrollLeft: 40, clientWidth: 1001 }, { offsetWidth: 176 }, 5)).toEqual({
      scrollLeft: 40,
      clientWidth: 1001,
      label: 176,
      plans: 5,
    });
  });

  it("reads a label that is not there yet as 0 wide", () => {
    expect(measureOf({ scrollLeft: 0, clientWidth: 934 }, null, 5).label).toBe(0);
  });
});

describe("seenFrom", () => {
  it("is what the region shows once it scrolls: the cue's count and the table's width", () => {
    const seen = seenFrom({ scrollLeft: 0, clientWidth: 934, label: 176, plans: 5 });
    expect(seen.scrolls).toBe(true);
    expect(seen.hidden).toBe(2);
    expect(seen.table).toBeCloseTo(176 + 5 * 236.67, 1);
  });

  it("leaves the table's width to its card while every plan fits", () => {
    expect(seenFrom({ scrollLeft: 0, clientWidth: 934, label: 176, plans: 3 })).toEqual({
      scrolls: false,
      hidden: 0,
      table: null,
    });
  });
});
