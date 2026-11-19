import { describe, expect, it } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import { EMPTY_FORM, formView, withStaged } from "../config/save-state";
import { SettingsCard } from "./config";

const SAVED = { autoMerge: null, avoidPeak: null, runsAtOnce: null, pollMinutes: 15 };

function noop() {}

describe("the Configuration card", () => {
  it("hands the Poll every control the interval the form shows", () => {
    const view = formView(SAVED, withStaged(EMPTY_FORM, "pollMinutes", 10, 15), false);
    const html = renderToStaticMarkup(
      <SettingsCard
        autonomy={{ status: "loading" }}
        peak={{ status: "loading" }}
        runsAtOnce={{ status: "loading" }}
        config={{
          status: "ready",
          url: "/api/config",
          body: { phases: [], concurrency: { review_wip: 3, platform_cap: 20 }, peak_windows: [] },
        }}
        runsPerRepo={{ status: "ready", url: "/api/runs-per-repo", body: { runs_per_repo: 5 } }}
        pollInterval={{ status: "ready", url: "/api/poll-interval", body: { poll_minutes: 15 } }}
        form={{
          ...view,
          saving: false,
          toggle: noop,
          setRunsAtOnce: noop,
          setPollMinutes: noop,
        }}
      />,
    );
    expect(html).toMatch(/aria-checked="true"[^>]*data-minutes="10"/);
    expect(html).toMatch(/aria-checked="false"[^>]*data-minutes="15"/);
  });
});
