import { afterEach, describe, expect, it, mock } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import type { Group } from "./types";

// A static render never measures, so the whisker's track width is set here, as
// the drawing's 418px bar column; 0 is what an unmeasured track reports.
let width = 418;
mock.module("./use-width", () => ({
  useWidth: () => ({ ref: { current: null }, width }),
}));
const { MetricPanel, StageCard } = await import("./metric-rows");

afterEach(() => {
  width = 418;
});

const noRest = () => "";

const percent = (value: number) => `${(value * 100).toFixed(1)}%`;

function groupOf(median: number | null, p90: number | null, rate = 0.5): Group {
  return {
    prs: 14,
    rate: percent(rate),
    rate_value: rate,
    median: median === null ? null : percent(median),
    median_value: median,
    p90: p90 === null ? null : percent(p90),
    p90_value: p90,
    agent_lines: 6000,
    owner_lines: 3000,
    review_found_lines: 2000,
    owner_change_lines: 700,
    unlabelled_lines: 300,
    no_agent_lines_prs: 0,
    no_agent_owner_lines: 0,
  };
}

const LAYOUTS = {
  "the desktop model row": (group: Group) => (
    <StageCard
      numeral={2}
      phase="build"
      through={14}
      total={14}
      models={[{ ...group, model: "deepseek-v4.1-flash" }]}
      width={1120}
    />
  ),
  "the phone model row": (group: Group) => (
    <StageCard
      numeral={2}
      phase="build"
      through={14}
      total={14}
      models={[{ ...group, model: "deepseek-v4.1-flash" }]}
      width={408}
    />
  ),
  "the desktop repository row": (group: Group) => (
    <MetricPanel
      rows={[{ key: "owner/p90-above", name: "owner/p90-above", group }]}
      split={false}
      wide
      sparse={false}
      restNote={noRest}
      padx={26}
    />
  ),
};

type Layout = keyof typeof LAYOUTS;

// The markup with React's text separators dropped, so words read as one string.
function render(layout: Layout, group: Group): string {
  return renderToStaticMarkup(LAYOUTS[layout](group)).replaceAll("<!-- -->", "");
}

function capX(markup: string): string | undefined {
  return /<line[^>]*x1="([^"]+)"[^>]*data-mark="whisker-cap"/.exec(markup)?.[1];
}

function markers(markup: string): number {
  return markup.split('data-mark="off-scale-p90"').length - 1;
}

const OFF_WORDS = "p90 off scale, above 80%";

for (const layout of Object.keys(LAYOUTS) as Layout[]) {
  describe(layout, () => {
    it("holds an off-scale p90's cap 3px inside the track and marks it beside the end", () => {
      const markup = render(layout, groupOf(0.402, 0.81));
      expect(capX(markup)).toBe("415");
      expect(markers(markup)).toBe(1);
    });

    it("says the p90 is off scale under the spread", () => {
      const markup = render(layout, groupOf(0.402, 1.0));
      const spread = markup.indexOf("median 40.2%, p90 100.0%");
      expect(spread).toBeGreaterThan(-1);
      expect(markup.indexOf(OFF_WORDS)).toBeGreaterThan(spread);
    });

    it("draws a p90 of exactly 80% 3px short, with no marker and no words", () => {
      const markup = render(layout, groupOf(0.402, 0.8));
      expect(capX(markup)).toBe("415");
      expect(markers(markup)).toBe(0);
      expect(markup).not.toContain(OFF_WORDS);
    });

    it("draws a p90 inside the axis where it falls, with no marker and no words", () => {
      const markup = render(layout, groupOf(0.402, 0.79));
      expect(capX(markup)).toBe("412.77500000000003");
      expect(markers(markup)).toBe(0);
      expect(markup).not.toContain(OFF_WORDS);
    });

    it("holds a median above the axis 6px inside the track with no marker of its own", () => {
      const markup = render(layout, groupOf(0.9, 0.95));
      expect(markup).toContain('<svg x="412"');
      expect(markers(markup)).toBe(1);
    });

    it("draws no whisker for a group with no p90", () => {
      expect(render(layout, groupOf(0.402, null))).not.toContain('data-mark="whisker"');
    });

    it("draws no whisker and no off-scale words for a group with no median", () => {
      const markup = render(layout, groupOf(null, 1.0));
      expect(markup).not.toContain('data-mark="whisker"');
      expect(markup).not.toContain(OFF_WORDS);
    });

    it("draws no whisker before its track is measured", () => {
      width = 0;
      expect(render(layout, groupOf(0.402, 1.0))).not.toContain('data-mark="whisker"');
    });
  });
}

describe("the pooled rate above the axis", () => {
  it("keeps its triangle and words on the desktop row", () => {
    const markup = render("the desktop repository row", groupOf(0.402, 0.6, 1.253));
    expect(markup).toContain('data-mark="off-scale"');
    expect(markup).toContain("125.3%");
    expect(markup).toContain("pooled, off scale");
  });

  it("keeps its note on the phone row", () => {
    const markup = renderToStaticMarkup(
      <MetricPanel
        rows={[
          { key: "owner/both-above", name: "owner/both-above", group: groupOf(0.7, 1.0, 1.253) },
        ]}
        split={false}
        wide={false}
        sparse={false}
        restNote={noRest}
        padx={16}
      />,
    );
    expect(markup).toContain('data-mark="off-scale"');
    expect(markup).toContain("Off scale: the pooled bar stops at 80%.");
  });
});

describe("the desktop words beside an off-scale p90", () => {
  const spreadClass = (markup: string) =>
    /<p class="([^"]*)">median 40\.2%/.exec(markup)?.[1].split(" ") ?? [];

  for (const layout of ["the desktop model row", "the desktop repository row"] as Layout[]) {
    it(`clear the triangle on ${layout}`, () => {
      expect(spreadClass(render(layout, groupOf(0.402, 1.0)))).toContain("pl-2");
      expect(spreadClass(render(layout, groupOf(0.402, 0.79)))).not.toContain("pl-2");
    });
  }
});

describe("the desktop repository row with no served spread text", () => {
  it("says nothing about the p90 when it cannot say the spread", () => {
    const group = { ...groupOf(0.402, 1.0), median: null, p90: null };
    expect(render("the desktop repository row", group)).not.toContain(OFF_WORDS);
  });
});
