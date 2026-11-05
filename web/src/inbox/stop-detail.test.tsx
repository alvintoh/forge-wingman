import { afterEach, describe, expect, it, mock } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import type { InboxRun } from "./run";
import { overflows, spansOf, stopDetailOf } from "./stop-detail";

// A static render never measures, so whether the sheet's detail overflows is set here; false is what
// an unmeasured region reports, as the real hook does. Nothing but the detail imports the hook.
let overflowing = false;
mock.module("./use-overflows", () => ({
  useOverflows: () => ({ ref: { current: null }, overflowing }),
}));
const { InboxRow } = await import("./row");
const { SheetStopDetail } = await import("./stop-detail-text");

afterEach(() => {
  overflowing = false;
});

const TIER = "command-code/xiaomi/mimo-v2.6-pro";
const ESCALATION = `Next model tier: ${TIER}. A \`model:${TIER}\` label on the ticket chooses it, and a retry then runs once on that tier.`;
const DETAIL = `${ESCALATION}\n\nThe build made no commits in 25 minutes.\n \nIts last step ran <b>go test</b>.`;

const stopped: InboxRun = {
  run: "run-1",
  ticket: "FRG-52",
  repo: "forge-wingman",
  pr_url: "",
  wait_reason: "",
  updated_at: "2026-11-05T00:00:00Z",
  title: "Cache the Linear projection per poll",
  stopped: true,
  retryable: true,
  stop_reason: "agent-no-progress",
};

function row(run: InboxRun): string {
  return renderToStaticMarkup(
    <InboxRow
      run={run}
      now={Date.parse("2026-11-05T00:06:00Z")}
      state={undefined}
      cancel={undefined}
      stop={undefined}
      onOpen={() => {}}
      onMerge={() => {}}
      onRetry={() => {}}
      onCompose={() => {}}
    />,
  );
}

describe("stopDetailOf", () => {
  it("splits at blank lines, one holding only spaces among them", () => {
    expect(stopDetailOf({ ...stopped, stop_detail: DETAIL })).toEqual([
      ESCALATION,
      "The build made no commits in 25 minutes.",
      "Its last step ran <b>go test</b>.",
    ]);
  });

  it("reads no paragraphs from an absent or blank detail", () => {
    expect(stopDetailOf(stopped)).toEqual([]);
    expect(stopDetailOf({ ...stopped, stop_detail: " \n\n " })).toEqual([]);
  });

  it("reads none of a run that waits on a merge", () => {
    expect(stopDetailOf({ ...stopped, stopped: false, stop_detail: DETAIL })).toEqual([]);
  });
});

describe("spansOf", () => {
  it("sets a span in backticks in mono with the backticks dropped", () => {
    expect(spansOf("A `model:x` label")).toEqual([
      { text: "A ", mono: false },
      { text: "model:x", mono: true },
      { text: " label", mono: false },
    ]);
  });

  it("reads a paragraph that opens with a span as that span alone", () => {
    expect(spansOf("`model:x`")).toEqual([{ text: "model:x", mono: true }]);
  });

  it("shows a paragraph with an odd number of backticks as written", () => {
    expect(spansOf("A `model:x` label `left open")).toEqual([
      { text: "A `model:x` label `left open", mono: false },
    ]);
  });
});

describe("a stopped row", () => {
  it("shows the first paragraph under its reason, clamped to three lines, the label in mono", () => {
    const html = row({ ...stopped, stop_detail: DETAIL });
    expect(html).toContain(
      `class="mt-[4.4px] line-clamp-3 text-cap leading-4 wrap-break-word text-ink2">Next model tier: ${TIER}. A <code class="font-mono wrap-anywhere text-ink">model:${TIER}</code> label on the ticket chooses it, and a retry then runs once on that tier.</p>`,
    );
    expect(html.indexOf("Stopped, the agent made no progress")).toBeLessThan(
      html.indexOf("Next model tier"),
    );
    expect(html).not.toContain("25 minutes");
    expect(html).not.toContain("go test");
  });

  it("describes the row by its stop reason alone, never its detail", () => {
    const html = row({ ...stopped, stop_detail: DETAIL });
    const ids = /aria-describedby="([^"]+)"/.exec(html)?.[1]?.split(" ") ?? [];
    const described = ids.map((id) => new RegExp(`id="${id}"[^>]*>([^<]*)`).exec(html)?.[1]);
    expect(described).toEqual(["Stopped, the agent made no progress"]);
  });

  it("shows markup in the detail as text", () => {
    const html = row({ ...stopped, stop_detail: "Its step ran <b>go test</b>." });
    expect(html).toContain("Its step ran &lt;b&gt;go test&lt;/b&gt;.");
  });

  it("describes the row by its reason alone, every named id present, with no detail", () => {
    const html = row(stopped);
    const ids = /aria-describedby="([^"]+)"/.exec(html)?.[1]?.split(" ") ?? [];
    expect(ids).toHaveLength(1);
    expect(html).toContain(`id="${ids[0]}"`);
  });

  it("names no description on a row with neither a reason nor a detail", () => {
    const pr: InboxRun = {
      ...stopped,
      stopped: false,
      stop_reason: undefined,
      pr_url: "https://github.com/alvintoh/forge-wingman/pull/7",
      head_sha: "77aa10c",
    };
    expect(row(pr)).not.toContain("aria-describedby");
  });

  it("renders as before with no detail", () => {
    const before = row(stopped);
    expect(before).not.toContain("line-clamp-3");
    expect(row({ ...stopped, stop_detail: "" })).toBe(before);
    expect(row({ ...stopped, stop_detail: " \n\n " })).toBe(before);
  });

  it("is a stopped run's only: a PR row shows no detail", () => {
    const pr: InboxRun = {
      ...stopped,
      stopped: false,
      pr_url: "https://github.com/alvintoh/forge-wingman/pull/7",
      stop_detail: DETAIL,
    };
    expect(row(pr)).not.toContain("Next model tier");
  });
});

describe("the sheet's detail", () => {
  it("shows the reason and every paragraph 8px apart, in a region named by the reason, scrolling past 14 lines", () => {
    const html = renderToStaticMarkup(
      <SheetStopDetail run={{ ...stopped, stop_detail: DETAIL }} />,
    );
    const reasonId = /<p id="([^"]+)"[^>]*>Stopped, the agent made no progress<\/p>/.exec(
      html,
    )?.[1];
    expect(reasonId).toBeDefined();
    expect(html).toContain(
      `<div role="region" aria-labelledby="${reasonId}" class="mt-[4.4px] max-h-56 space-y-2 overflow-y-auto rounded-tile">`,
    );
    expect(
      html.match(/<p class="whitespace-pre-line text-cap leading-4 wrap-break-word text-ink2">/g)
        ?.length,
    ).toBe(3);
    expect(html).toContain(`<code class="font-mono wrap-anywhere text-ink">model:${TIER}</code>`);
    expect(html).toContain("The build made no commits in 25 minutes.");
    expect(html).toContain("Its last step ran &lt;b&gt;go test&lt;/b&gt;.");
  });

  it("keeps a paragraph's single line breaks, as a raw git error's lines are", () => {
    const html = renderToStaticMarkup(
      <SheetStopDetail
        run={{
          ...stopped,
          stop_detail: "error: could not apply 1a2b3c\nhint: resolve the conflict",
        }}
      />,
    );
    expect(html).toContain(
      '<p class="whitespace-pre-line text-cap leading-4 wrap-break-word text-ink2">error: could not apply 1a2b3c\nhint: resolve the conflict</p>',
    );
  });

  it("takes the focus only while its region scrolls", () => {
    const run = { ...stopped, stop_detail: DETAIL };
    expect(renderToStaticMarkup(<SheetStopDetail run={run} />)).not.toContain("tabindex");
    overflowing = true;
    expect(renderToStaticMarkup(<SheetStopDetail run={run} />)).toMatch(
      /<div role="region" aria-labelledby="[^"]+" tabindex="0" class="mt-\[4\.4px\] max-h-56/,
    );
  });

  it("scrolls only when its content is taller than the region", () => {
    expect(overflows({ scrollHeight: 225, clientHeight: 224 })).toBe(true);
    expect(overflows({ scrollHeight: 224, clientHeight: 224 })).toBe(false);
  });

  it("shows nothing for a run with no detail, or one that waits on a merge", () => {
    expect(renderToStaticMarkup(<SheetStopDetail run={stopped} />)).toBe("");
    expect(
      renderToStaticMarkup(
        <SheetStopDetail run={{ ...stopped, stopped: false, stop_detail: DETAIL }} />,
      ),
    ).toBe("");
  });
});
