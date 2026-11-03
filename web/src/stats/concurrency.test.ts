import { describe, expect, it } from "bun:test";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { isConcurrency, peakReachedN, type Concurrency } from "./concurrency";
import { ConcurrencyCard, ConcurrencyCardView } from "./concurrency-card";
import type { Read } from "./use-read";

// concurrency is the endpoint's answer with the peak below N and two reasons held; a case overrides
// what it varies.
const concurrency = (over: Partial<Concurrency> = {}): Concurrency => ({
  peak: 1,
  runs_at_once: 2,
  runs_per_repo: 5,
  review_wip: 3,
  platform_cap: 20,
  held: [
    { reason: "concurrency", count: 9 },
    { reason: "budget", count: 4 },
  ],
  ...over,
});

const render = (read: Read<Concurrency>) =>
  renderToStaticMarkup(createElement(ConcurrencyCardView, { read }));
const ready = (body: Concurrency) => render({ status: "ready", body, url: "/api/concurrency" });

// text is the markup with its tags dropped, so a test reads what a sighted reader sees in order.
const text = (html: string) => html.replace(/<[^>]+>/g, " ").replace(/\s+/g, " ");

describe("isConcurrency", () => {
  it("accepts the endpoint's answer", () => {
    expect(isConcurrency(concurrency())).toBe(true);
  });

  it("accepts an answer with no run held", () => {
    expect(isConcurrency(concurrency({ held: [] }))).toBe(true);
  });

  it("accepts a peak of zero", () => {
    expect(isConcurrency(concurrency({ peak: 0 }))).toBe(true);
  });

  it("rejects a limit below one", () => {
    expect(isConcurrency(concurrency({ runs_at_once: 0 }))).toBe(false);
  });

  it("rejects a figure that is not a whole number", () => {
    expect(isConcurrency(concurrency({ peak: 1.5 }))).toBe(false);
    expect(isConcurrency({ ...concurrency(), review_wip: "3" })).toBe(false);
  });

  it("rejects a held reason with no count", () => {
    expect(isConcurrency(concurrency({ held: [{ reason: "blocked", count: 0 }] }))).toBe(false);
  });

  it("rejects an answer without its held list", () => {
    const { held: _held, ...body } = concurrency();
    expect(isConcurrency(body)).toBe(false);
  });

  it("rejects what is not an object", () => {
    expect(isConcurrency(null)).toBe(false);
  });
});

describe("peakReachedN", () => {
  it("is met when the peak equals N", () => {
    expect(peakReachedN(concurrency({ peak: 2 }))).toBe(true);
  });

  it("is met when the peak passed an N lowered since", () => {
    expect(peakReachedN(concurrency({ peak: 3 }))).toBe(true);
  });

  it("is not met below N", () => {
    expect(peakReachedN(concurrency({ peak: 1 }))).toBe(false);
  });
});

describe("ConcurrencyCard with an answer", () => {
  it("names the card and defines it", () => {
    const html = ready(concurrency());
    expect(html).toContain("concurrency</h2>");
    expect(html).toContain(
      "Peak is the most runs in flight at once in the trailing 30 days, beside each limit.",
    );
    expect(html).toContain('aria-busy="false"');
  });

  it("shows the peak, then the four limits in order with their values", () => {
    expect(text(ready(concurrency()))).toContain(
      "Peak in flight, 30 days 1 Runs at once (N) 2 Runs per repo 5 Review WIP 3 GitHub job limit 20",
    );
  });

  it("says in words when the peak reached N, and only then", () => {
    const line = "The peak reached runs at once (N).";
    expect(ready(concurrency({ peak: 2 }))).toContain(line);
    expect(ready(concurrency({ peak: 1 }))).not.toContain(line);
  });

  it("lists the held reasons in the order served, each in mono beside its count", () => {
    const html = ready(concurrency());
    expect(text(html)).toContain("runs held, 30 days concurrency 9 budget 4");
    expect(html).toContain('<dt class="min-w-0 font-mono text-ink">budget</dt>');
    expect(html).not.toContain("No run was held.");
  });

  it("says no run was held when none was", () => {
    const html = ready(concurrency({ held: [] }));
    expect(text(html)).toContain("runs held, 30 days No run was held.");
  });
});

describe("ConcurrencyCard while it reads", () => {
  const html = renderToStaticMarkup(createElement(ConcurrencyCard));

  it("says it is reading, busy, in the peak's place", () => {
    expect(html).toContain('aria-busy="true"');
    expect(html).toContain(
      '<p role="status" class="min-w-0 text-label text-ink2">Reading the concurrency…</p>',
    );
  });

  it("draws each limit's label with its value empty", () => {
    for (const label of ["Runs at once (N)", "Runs per repo", "Review WIP", "GitHub job limit"]) {
      expect(html).toContain(
        `<dt class="min-w-0 text-ink2">${label}</dt><dd class="min-w-[2ch] text-right font-semibold text-ink"></dd>`,
      );
    }
  });

  it("holds the held list's and the peak line's height unseen and out of the reading order", () => {
    expect(html).toContain('aria-hidden="true" class="mt-0.75 invisible"');
    expect(html).toContain(
      'aria-hidden="true" class="mt-0.5 text-cap leading-4 text-ink2 invisible"',
    );
    expect(html).not.toContain("Peak in flight, 30 days");
  });
});

describe("ConcurrencyCard when the read fails", () => {
  const html = render({ status: "error", url: "/api/concurrency" });

  it("says so under the head and draws nothing else", () => {
    expect(html).toContain('role="alert"');
    expect(html).toContain("The concurrency could not be read.");
    expect(html).not.toContain("Runs at once (N)");
    expect(html).not.toContain("runs held, 30 days");
  });
});
