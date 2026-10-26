import { useEffect } from "react";
import { ByModel } from "./by-model";
import { ByRepo, RepoMultiples } from "./by-repo";
import { BySize } from "./by-size";
import { DataDisclosure } from "./data-table";
import { isRewrite } from "./guard";
import { PatternDefs } from "./marks";
import { Tiles } from "./tiles";
import { TrendCard } from "./trend-chart";
import type { RewriteScope, RewriteView } from "./types";

// A state replaces the charts with one card saying what there is to read.
function StateCard({ title, words }: { title: string; words: string }) {
  return (
    <div className="card-frame py-6 max-sm:-mx-2.5">
      <p className="text-title font-semibold text-ink2">{title}</p>
      <p className="mt-2 text-label text-muted">{words}</p>
    </div>
  );
}

type RewriteFiguresProps = {
  // What the endpoint answered under "rewrite", or null for a read already reported as failed.
  figures: unknown;
  scope: RewriteScope | null;
  // The figures on screen are being replaced by another repository's.
  busy: boolean;
};

// RewriteFigures draws the rewrite section from what the endpoint answered under
// "rewrite", or says it could not read it, so a document of another shape costs
// the page that section and not the auto-merge state above it.
export function RewriteFigures({ figures, scope, busy }: RewriteFiguresProps) {
  const unreadable = figures !== null && !isRewrite(figures);
  useEffect(() => {
    if (unreadable) {
      console.error(
        "rewriteNotRead",
        "the autonomy endpoint answered rewrite figures of another shape",
      );
    }
  }, [unreadable]);
  if (!isRewrite(figures)) {
    return <p role="alert">The rewrite figures could not be read.</p>;
  }
  return <RewriteSection rewrite={figures} scope={scope} busy={busy} />;
}

type RewriteSectionProps = { rewrite: RewriteView; scope: RewriteScope | null; busy: boolean };

// RewriteSection is the rewrite rate as a dashboard: the figures, the trend with
// its per-PR strip, the split by size, model and repository, and the numbers
// behind them. It draws what the endpoint served and computes only geometry and
// the words around a figure. With no measured PR, or none with agent lines, it is
// one card; under ten PRs the trend is one line and the rest is drawn from what
// there is.
export function RewriteSection({ rewrite, scope, busy }: RewriteSectionProps) {
  const first = rewrite.tiles[0];
  const bar = rewrite.bars.rollback;
  const trended = rewrite.by_repo.filter((repo) => repo.series.length > 0);
  const partial = first !== undefined && first.prs.length < first.asked;
  // Nothing to divide only where no PR in the read has agent lines: the newest ten
  // having none leaves older ones, and the charts and tables are drawn from them.
  const nothingToDivide = rewrite.by_size.every((size) => size.agent_lines === 0);
  const tiles = (
    <Tiles tiles={rewrite.tiles} prs={rewrite.prs} bar={bar} bleed={!partial} stacked={partial} />
  );
  const trendAside =
    scope?.repo ?? (trended.length > 1 ? "gate, pools every repository" : "all repositories");
  return (
    <section aria-labelledby="rewrite-title" aria-busy={busy}>
      <h2 id="rewrite-title" className="text-head font-semibold break-words text-ink">
        {scope === null ? "Rewrite rate" : `Rewrite rate, ${scope.repo}`}
      </h2>
      <p className="mt-1 text-body text-ink2">Owner lines as a share of agent lines.</p>
      {scope !== null && scope.total > 0 && (
        <p className="mt-0.5 text-label font-semibold break-words text-ink">
          {scope.repo} only: {scope.measured} of the {scope.total} measured{" "}
          {scope.total === 1 ? "PR" : "PRs"}.
        </p>
      )}
      <PatternDefs />
      {rewrite.prs.length === 0 && (
        <div className="mt-4">
          <StateCard
            title="no measured PRs"
            words={
              scope === null
                ? "Rates appear once a runner PR has merged."
                : `Rates for ${scope.repo} appear once one of its runner PRs has merged.`
            }
          />
        </div>
      )}
      {rewrite.prs.length > 0 && nothingToDivide && (
        <div className="mt-4">
          <StateCard
            title="no agent lines"
            words={
              scope === null
                ? "Merged PRs carry no agent lines, so there is nothing to divide."
                : `${scope.repo}'s merged PRs carry no agent lines, so there is nothing to divide.`
            }
          />
        </div>
      )}
      {rewrite.prs.length > 0 && first !== undefined && !nothingToDivide && (
        <div className="mt-4 space-y-6">
          {!partial && tiles}
          {trended.length > 1 && <RepoMultiples repos={trended} bar={bar} />}
          <TrendCard
            series={rewrite.series}
            prs={rewrite.prs}
            bar={bar}
            aside={trendAside}
            lead={partial ? tiles : undefined}
          />
          <BySize sizes={rewrite.by_size} bar={bar} />
          <ByModel byModel={rewrite.by_model} />
          <ByRepo repos={rewrite.by_repo} />
          <DataDisclosure rewrite={rewrite} />
        </div>
      )}
    </section>
  );
}
