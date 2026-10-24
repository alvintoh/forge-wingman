import { captionClass } from "../caption";
import { SPARSE_W, WIDE_ROWS, buildAxis, mergeSlots, padxFor } from "./axis";
import { ChartCard, ChartHeader } from "./chart-card";
import { prsWord, seriesLabel, servedRate } from "./format";
import { MetricPanel } from "./metric-rows";
import type { RowSpec } from "./metric-rows";
import { TrendPlot } from "./trend-chart";
import type { RepoRow } from "./types";

// ByRepo is each repository's average over all its measured PRs, whether or not
// it has a trend, so a repository too new to trend still has a figure.
export function ByRepo({ repos }: { repos: RepoRow[] }) {
  const rows: RowSpec[] = repos.map((repo) => ({
    key: repo.repo,
    name: repo.repo,
    group: repo.average,
    recorded: true,
  }));
  return (
    <ChartCard>
      {(width) => {
        const padx = padxFor(width);
        return (
          <div>
            <ChartHeader title="by repository" padx={padx} />
            <MetricPanel
              rows={rows}
              split={false}
              wide={width >= WIDE_ROWS}
              sparse={width < SPARSE_W}
              restNote={restNote}
              padx={padx}
            />
            <p className="mt-3 text-cap text-ink2" style={{ paddingInline: padx }}>
              Average is pooled over the repository's measured PRs.
            </p>
          </div>
        );
      }}
    </ChartCard>
  );
}

function restNote(rest: RowSpec[]): string {
  const first = rest[0];
  if (rest.length === 1 && first !== undefined) {
    return `Another repository, ${first.name}, has ${prsWord(first.group.prs)}, under ten, so no median.`;
  }
  return `${rest.length} other repositories have under ten PRs, so they show no median.`;
}

const AVERAGE_GUTTER = 120;
const MULTIPLE_PH = 130;

type RepoMultiplesProps = { repos: RepoRow[]; bar: number };

// RepoMultiples draws a small trend for each repository that has one, each on
// its own merge order, with the repository's average beside it. It is drawn only
// for two or more repositories with a trend: a repository under ten PRs has no
// series to draw, and one trended repository is the main trend itself.
export function RepoMultiples({ repos, bar }: RepoMultiplesProps) {
  return (
    <ChartCard>
      {(width) => {
        const padx = padxFor(width);
        const columns = width >= WIDE_ROWS ? 2 : 1;
        const columnWidth = width / columns;
        const gutter = columns === 2 ? AVERAGE_GUTTER : 0;
        return (
          <div>
            <ul
              className={
                columns === 2 ? "grid grid-cols-2 divide-x divide-hair" : "divide-y divide-hair"
              }
            >
              {repos.map((repo) => {
                const axis = buildAxis(mergeSlots(repo.series, []), {
                  width: columnWidth,
                  padx,
                  gutter,
                });
                const average = repo.average;
                return (
                  <li key={repo.repo} className="relative py-3" data-repo={repo.repo}>
                    <div
                      className="flex flex-wrap items-baseline justify-between gap-x-4"
                      style={{ paddingInline: padx }}
                    >
                      <h3 className={`${captionClass} text-ink2`}>{repo.repo}</h3>
                      {columns === 1 && (
                        <p className="text-cap text-ink2">
                          average {servedRate(average.rate, average.rate_value, "no rate")},{" "}
                          {prsWord(average.prs)}
                        </p>
                      )}
                    </div>
                    <div className="relative mt-3">
                      <TrendPlot
                        axis={axis}
                        width={columnWidth}
                        series={repo.series}
                        bar={bar}
                        height={MULTIPLE_PH}
                        dates={false}
                        sample={false}
                        label={`${repo.repo}: ${seriesLabel(repo.series)}`}
                      />
                      {columns === 2 && (
                        <div className="absolute top-8" style={{ left: axis.x1 + 12 }}>
                          <p className="text-cap font-semibold text-ink2">average</p>
                          <p className="mt-3 font-mono text-head font-semibold text-ink">
                            {servedRate(average.rate, average.rate_value, "no rate")}
                          </p>
                          <p className="text-label text-ink2">{prsWord(average.prs)}</p>
                        </div>
                      )}
                    </div>
                  </li>
                );
              })}
            </ul>
            <p className="pb-1 text-cap text-ink2" style={{ paddingInline: padx }}>
              The gate pools every repository. The split is informational and never moves the gate.
            </p>
          </div>
        );
      }}
    </ChartCard>
  );
}
