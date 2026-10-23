import { ByModel } from "./by-model";
import { ByRepo, RepoMultiples } from "./by-repo";
import { BySize } from "./by-size";
import { DataDisclosure } from "./data-table";
import { PatternDefs } from "./marks";
import { Tiles } from "./tiles";
import { TrendCard } from "./trend-chart";
import type { RewriteView } from "./types";

// A state replaces the charts with one card saying what there is to read.
function StateCard({ title, words }: { title: string; words: string }) {
  return (
    <div className="card-frame py-6 max-sm:-mx-2.5">
      <p className="text-title font-semibold text-ink2">{title}</p>
      <p className="mt-2 text-label text-muted">{words}</p>
    </div>
  );
}

type RewriteSectionProps = { rewrite: RewriteView };

// RewriteSection is the rewrite rate as a dashboard: the figures, the trend with
// its per-PR strip, the split by size, model and repository, and the numbers
// behind them. It draws what the endpoint served and computes only geometry and
// the words around a figure. With no measured PR, or none with agent lines, it is
// one card; under ten PRs the trend is one line and the rest is drawn from what
// there is.
export function RewriteSection({ rewrite }: RewriteSectionProps) {
  const first = rewrite.tiles[0];
  const bar = rewrite.bars.rollback;
  const trended = rewrite.by_repo.filter((repo) => repo.series.length > 0);
  const partial = first !== undefined && first.prs.length < first.asked;
  const tiles = (
    <Tiles tiles={rewrite.tiles} prs={rewrite.prs} bar={bar} bleed={!partial} stacked={partial} />
  );
  return (
    <section aria-labelledby="rewrite-title" className="mt-10">
      <h2 id="rewrite-title" className="text-head font-semibold text-ink">
        Rewrite rate
      </h2>
      <p className="mt-1 text-body text-ink2">Owner lines as a share of agent lines.</p>
      <PatternDefs />
      {rewrite.prs.length === 0 && (
        <div className="mt-4">
          <StateCard title="no measured PRs" words="Rates appear once a runner PR has merged." />
        </div>
      )}
      {rewrite.prs.length > 0 && first !== undefined && first.agent_lines === 0 && (
        <div className="mt-4">
          <StateCard
            title="no agent lines"
            words="Merged PRs carry no agent lines, so there is nothing to divide."
          />
        </div>
      )}
      {rewrite.prs.length > 0 && first !== undefined && first.agent_lines > 0 && (
        <div className="mt-4 space-y-6">
          {!partial && tiles}
          {trended.length > 1 && <RepoMultiples repos={trended} bar={bar} />}
          <TrendCard
            series={rewrite.series}
            prs={rewrite.prs}
            bar={bar}
            aside={trended.length > 1 ? "gate, pools every repository" : "all repositories"}
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
