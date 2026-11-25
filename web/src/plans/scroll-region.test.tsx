import { describe, expect, it } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import { ScrollRegion } from "./scroll-region";

function markup(scrolls: boolean, hidden: number): string {
  return renderToStaticMarkup(
    <ScrollRegion regionRef={null} scrolls={scrolls} hidden={hidden}>
      <table />
    </ScrollRegion>,
  );
}

describe("ScrollRegion", () => {
  it("is a named, focusable region once the table scrolls", () => {
    const html = markup(true, 2);
    expect(html).toContain('role="region"');
    expect(html).toContain('aria-label="Plans table, scrolls sideways"');
    expect(html).toContain('tabindex="0"');
    expect(html).toContain("overflow-x-auto");
  });

  it("is no tab stop and no region while the table fits", () => {
    const html = markup(false, 0);
    expect(html).not.toContain("tabindex");
    expect(html).not.toContain('role="region"');
    expect(html).not.toContain("aria-label");
  });

  it("keeps the scroll from chaining to the page's own horizontal scroll", () => {
    expect(markup(true, 2)).toContain("overscroll-x-contain");
  });

  it("shows the plans left to reach in words and a chevron, and describes the region by it", () => {
    const html = markup(true, 3);
    expect(html).toMatch(/<p id="compare-card-cue"[^>]*>.*\+3 plans.*<svg aria-hidden="true"/);
    expect(html).toContain('aria-describedby="compare-card-cue"');
    expect(html).toContain('<path d="M1 1l4 4l-4 4"></path>');
  });

  it("says one plan in the singular", () => {
    expect(markup(true, 1)).toContain("+1 plan<");
  });

  it("shows no cue, and describes nothing, once no plan is left to reach", () => {
    const html = markup(true, 0);
    expect(html).not.toContain("compare-card-cue");
    expect(html).not.toContain("plans<");
    expect(html).not.toContain("aria-describedby");
  });

  it("keeps a 3rem gutter after a scrolling table, and none after one that fits", () => {
    expect(markup(true, 2)).toContain('<div class="w-max pr-12"><table>');
    expect(markup(false, 0)).not.toContain("pr-12");
  });

  it("covers the gutter in the card's fill while plans lie past the edge, and not at the end", () => {
    expect(markup(true, 2)).toContain("absolute inset-y-0 right-0 z-20 w-12 bg-card");
    expect(markup(true, 0)).not.toContain("w-12 bg-card");
  });

  it("leaves the cue out of the pointer's way, so a touch on it still scrolls the table", () => {
    expect(markup(true, 2)).toContain("pointer-events-none");
  });
});
