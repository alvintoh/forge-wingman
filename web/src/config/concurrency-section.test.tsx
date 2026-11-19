import { describe, expect, it } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import type { Read } from "../stats/use-read";
import { ConcurrencySection } from "./concurrency-section";
import type { ConfigView, PollIntervalView, RunsPerRepoView } from "./types";

const CONFIG: Read<ConfigView> = {
  status: "ready",
  url: "/api/config",
  body: { phases: [], concurrency: { review_wip: 3, platform_cap: 20 }, peak_windows: [] },
};
const RUNS_PER_REPO: Read<RunsPerRepoView> = {
  status: "ready",
  url: "/api/runs-per-repo",
  body: { runs_per_repo: 5 },
};

function poll(minutes: number): Read<PollIntervalView> {
  return { status: "ready", url: "/api/poll-interval", body: { poll_minutes: minutes } };
}

function draw(
  pollInterval: Read<PollIntervalView>,
  pollMinutes: number | null,
  config: Read<ConfigView> = CONFIG,
): string {
  return renderToStaticMarkup(
    <ConcurrencySection
      config={config}
      runsPerRepo={RUNS_PER_REPO}
      pollInterval={pollInterval}
      pollMinutes={pollMinutes}
      disabled={false}
      onPollChoose={() => {}}
    />,
  );
}

describe("the Concurrency section", () => {
  it("says only Poll every is the owner's and names the control by its label", () => {
    const html = draw(poll(10), 10);
    expect(html).toContain("Set on the dispatcher, except Poll every.");
    expect(html).not.toContain("Read-only; set on the dispatcher.");
    expect(html).toMatch(/<dt id="config-poll-every"[^>]*>Poll every<\/dt>/);
    expect(html).toContain('aria-labelledby="config-poll-every"');
    expect(html).toMatch(/aria-checked="true"[\s\S]*?>10 minutes</);
  });

  it("ticks 15 minutes, the interval the endpoint answers before one is saved", () => {
    expect(draw(poll(15), 15)).toMatch(/aria-checked="true"[\s\S]*?>15 minutes</);
  });

  it("draws the control inactive with nothing ticked while the interval loads", () => {
    const html = draw({ status: "loading" }, null);
    expect(html).toContain('aria-busy="true"');
    expect(html).toContain("Reading the concurrency…");
    expect(html).not.toContain("Set on the dispatcher");
    expect(html).toContain(
      'role="radiogroup" aria-labelledby="config-poll-every" aria-disabled="true"',
    );
    expect(html).not.toContain('aria-checked="true"');
  });

  it("hides the control with the rows when the interval cannot be read", () => {
    const html = draw({ status: "error", url: "/api/poll-interval" }, null);
    expect(html).toContain("The concurrency could not be read.");
    expect(html).not.toContain("radiogroup");
    expect(html).not.toContain("Review WIP");
  });

  it("hides the control when another of its reads fails", () => {
    const html = draw(poll(15), 15, { status: "error", url: "/api/config" });
    expect(html).toContain("The concurrency could not be read.");
    expect(html).not.toContain("radiogroup");
  });
});
