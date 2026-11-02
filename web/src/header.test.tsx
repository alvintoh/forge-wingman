import { describe, expect, it } from "bun:test";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { AttentionLine } from "./header";
import type { Inbox } from "./inbox";
import type { InboxRun } from "./inbox/run";

function runs(count: number): InboxRun[] {
  return Array.from({ length: count }, (_, i) => ({
    run: `run-${i}`,
    ticket: `T-${i}`,
    repo: "acme/widgets",
    pr_url: "",
    wait_reason: "draft",
    updated_at: "2026-03-01T12:00:00Z",
  }));
}

function draw(status: Inbox["state"]["status"], count = 0): string {
  const state = { status, runs: runs(count), readAt: 0, stale: false };
  return renderToStaticMarkup(createElement(AttentionLine, { state }));
}

// words is the line as read: the markup's text, React's text-node separators dropped.
function words(markup: string): string {
  return markup.replace(/<!-- -->/g, "").replace(/<[^>]+>/g, "");
}

describe("AttentionLine", () => {
  it("leads several runs with their count in the stop colour", () => {
    const markup = draw("ready", 5);
    expect(words(markup)).toBe("5 runs need your attention");
    expect(markup).toContain('<span class="mr-1 font-semibold text-stop">5</span>');
    expect(markup).toContain("text-ink2");
  });

  it("reads one run in the singular", () => {
    expect(words(draw("ready", 1))).toBe("1 run needs your attention");
  });

  it("says nothing needs attention when the read found no runs", () => {
    const markup = draw("ready");
    expect(words(markup)).toBe("Nothing needs your attention");
    expect(markup).not.toContain("text-stop");
  });

  it("stays empty at its height while the first read is open", () => {
    const markup = draw("loading");
    expect(words(markup)).toBe("");
    expect(markup).toMatch(/<p class="[^"]*\bh-6\b[^"]*"><\/p>/);
  });

  it("says the inbox could not be read, never that nothing needs attention", () => {
    expect(words(draw("error"))).toBe("The inbox could not be read.");
  });
});
