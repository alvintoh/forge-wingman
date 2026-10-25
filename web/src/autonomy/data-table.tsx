import type { ReactNode } from "react";
import { parsePull } from "../inbox/run";
import { prKey } from "./axis";
import {
  NO_RATE,
  count,
  isPrsWord,
  isRed,
  lastPrRepo,
  modelName,
  noAgentNote,
  percent,
  phaseWord,
  prsWord,
  servedRate,
  spreadText,
  trendWords,
  utcDay,
  whole,
} from "./format";
import { PHASES } from "./types";
import type { Group, ModelRow, Phase, PR, RewriteView, SizeRow } from "./types";

const TILE_ROWS = ["trailing 10", "last 5", "last PR"];

type Column<T> = {
  head: string;
  render: (row: T) => ReactNode;
  // Numbers are mono and right-aligned beside the table's other figures.
  figure?: boolean;
  // Words too long for half a stacked row take the whole of it.
  long?: boolean;
};

type TableProps<T> = {
  title: string;
  columns: Column<T>[];
  rows: T[];
  rowKey: (row: T) => string;
};

// DataTable is one table of the Data disclosure. Its first column heads each row,
// and below the md breakpoint a row stacks, each cell naming itself, so the table
// needs no sideways scroll on a phone.
function DataTable<T>({ title, columns, rows, rowKey }: TableProps<T>) {
  const [first, ...rest] = columns;
  if (first === undefined) {
    return null;
  }
  // The 5px at the left keeps a link's 3px focus ring and its 2px offset inside the scroller.
  return (
    <div className="-ml-[5px] overflow-x-auto pl-[5px]">
      <table className="w-full text-label max-md:block">
        <caption className="pb-1 text-left text-cap font-semibold text-ink2 max-md:block">
          {title}
        </caption>
        <thead className="max-md:sr-only">
          <tr className="border-b border-hair text-left text-cap text-ink2">
            {columns.map((column) => (
              <th
                key={column.head}
                scope="col"
                className={`py-2 pr-4 font-semibold ${column.figure ? "text-right" : ""}`}
              >
                {column.head}
              </th>
            ))}
          </tr>
        </thead>
        <tbody className="max-md:block">
          {rows.map((row) => (
            <tr
              key={rowKey(row)}
              className="border-b border-hair max-md:grid max-md:grid-cols-2 max-md:gap-x-4 max-md:py-2"
            >
              <th
                scope="row"
                className="py-2 pr-4 text-left font-normal max-md:col-span-2 max-md:block max-md:py-1"
              >
                {first.render(row)}
              </th>
              {rest.map((column) => (
                <td
                  key={column.head}
                  className={`py-2 pr-4 max-md:py-1 max-md:pr-0 max-md:before:text-cap max-md:before:text-ink2 max-md:before:content-[attr(data-label)] ${column.figure ? "md:text-right" : ""} ${column.long ? "max-md:col-span-2 max-md:block max-md:before:block" : "max-md:flex max-md:items-baseline max-md:justify-between max-md:gap-2"}`}
                  data-label={column.head}
                >
                  <Figure on={column.figure}>{column.render(row)}</Figure>
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// Figure sets a number in mono, and leaves words, which are not figures, as they are. A PR
// count and "no rate" are dimmed words, and a count says it is under ten to a reader of the text.
function Figure({ on, children }: { on: boolean | undefined; children: ReactNode }) {
  const count = typeof children === "string" && isPrsWord(children);
  const figure =
    on &&
    (typeof children === "number" ||
      (typeof children === "string" && /\d/.test(children) && !count));
  const dim = count || children === NO_RATE;
  return (
    <span className={figure ? "font-mono" : dim ? "text-ink2 lg:whitespace-nowrap" : undefined}>
      {children}
      {count && <span className="sr-only"> (under ten PRs)</span>}
    </span>
  );
}

// Rate prints a rate as the endpoint printed it, red beside a triangle when it is
// above the bar; the triangle is a picture, so its words follow it for a reader of
// the text.
function Rate({
  value,
  text,
  words,
  bar,
}: {
  value: number | null;
  text: string | null;
  words: string;
  bar: number;
}) {
  const printed = servedRate(text, value, words);
  if (!isRed(value, bar)) {
    return (
      <span className={value === null ? "font-semibold" : "font-mono font-semibold"}>
        {printed}
      </span>
    );
  }
  return (
    <span className="inline-flex items-center gap-1.5 font-mono font-semibold text-stop">
      <svg width="10" height="10" viewBox="0 0 10 10" aria-hidden="true" className="shrink-0">
        <polygon points="5,0.5 0,9.5 10,9.5" className="fill-stop" />
      </svg>
      {printed}
      <span className="sr-only"> (above {whole(bar)})</span>
    </span>
  );
}

function PRNumber({ pr, showRepo }: { pr: PR; showRepo: boolean }) {
  const repo = showRepo ? (
    <span className="block font-mono text-micro text-ink2">{pr.repo}</span>
  ) : null;
  if (parsePull(pr.pr_url) === null) {
    return (
      <>
        <span className="font-mono font-semibold">#{pr.pr}</span>
        {repo}
      </>
    );
  }
  return (
    <>
      <a
        href={pr.pr_url}
        target="_blank"
        rel="noopener noreferrer"
        className="inline-flex min-h-11 min-w-11 items-center font-mono font-semibold text-accent underline"
      >
        #{pr.pr}
        <span className="sr-only"> (opens in a new tab)</span>
      </a>
      {repo}
    </>
  );
}

function modelCell(phase: Phase, id: string): ReactNode {
  return (
    <>
      <span>{phaseWord(phase, id)}</span>
      {modelName(id) !== id && <span className="block font-mono text-micro text-ink2">{id}</span>}
    </>
  );
}

type ModelTableRow = { phase: Phase; row: ModelRow };

function groupColumns(bar: number): Column<Group>[] {
  return [
    { head: "PRs", render: (g) => g.prs, figure: true },
    { head: "Owner lines", render: (g) => count(g.owner_lines), figure: true },
    { head: "Agent lines", render: (g) => count(g.agent_lines), figure: true },
    {
      head: "Pooled",
      render: (g) => <Rate value={g.rate_value} text={g.rate} words="no rate" bar={bar} />,
      figure: true,
    },
    { head: "Median PR", render: (g) => spreadText(g.median, g), figure: true },
    { head: "p90 PR", render: (g) => spreadText(g.p90, g), figure: true },
  ];
}

function joinNames(names: string[]): string {
  const last = names[names.length - 1];
  return names.length < 2 || last === undefined
    ? names.join("")
    : `${names.slice(0, -1).join(", ")} and ${last}`;
}

// noAgentNotes says which pooled rates carry PRs with no agent lines, once per
// figure the PRs account for, however many groups hold them.
function noAgentNotes(rewrite: RewriteView): string[] {
  const named: { name: string; group: Group }[] = [
    ...PHASES.flatMap((phase) =>
      rewrite.by_model[phase].map((row) => ({ name: phaseWord(phase, row.model), group: row })),
    ),
    ...rewrite.by_repo.map((repo) => ({ name: repo.repo, group: repo.average })),
  ];
  const byFigure = new Map<string, { prs: number; lines: number; names: string[] }>();
  for (const { name, group } of named) {
    if (noAgentNote(name, group) === null) {
      continue;
    }
    const key = `${group.no_agent_lines_prs}:${group.no_agent_owner_lines}`;
    const entry = byFigure.get(key) ?? {
      prs: group.no_agent_lines_prs,
      lines: group.no_agent_owner_lines,
      names: [],
    };
    if (!entry.names.includes(name)) {
      entry.names.push(name);
    }
    byFigure.set(key, entry);
  }
  return [...byFigure.values()].map(
    (entry) =>
      `${prsWord(entry.prs)} with no agent lines: ${count(entry.lines)} owner lines (in the ${joinNames(entry.names)} pooled rates).`,
  );
}

type DataDisclosureProps = { rewrite: RewriteView };

// DataDisclosure holds the exact numbers behind every chart, which are the
// charts' text alternative: it is closed until opened and is the same set of
// tables at every width. A rate above the bar is red with a triangle, and the
// legend says what red means.
export function DataDisclosure({ rewrite }: DataDisclosureProps) {
  const bar = rewrite.bars.rollback;
  const sizes = rewrite.by_size.filter((size) => size.prs > 0);
  const models: ModelTableRow[] = PHASES.flatMap((phase) =>
    rewrite.by_model[phase].map((row) => ({ phase, row })),
  );
  const tileRows = rewrite.tiles.map((tile, i) => ({ tile, label: TILE_ROWS[i] ?? "" }));
  const lastPR = rewrite.tiles[2]?.prs[0];
  const lastRepo = lastPR === undefined ? null : lastPrRepo(rewrite.prs, lastPR);
  const notes = noAgentNotes(rewrite);
  // A PR's number names it only within its repository, so rows show the repository
  // where the lists span more than one.
  const manyRepos = new Set(rewrite.prs.map((pr) => pr.repo)).size > 1;
  const pointRepos = new Set(rewrite.series.map((point) => point.repo)).size;
  return (
    <details className="group rounded-tile border border-hair bg-card" data-disclosure>
      <summary className="flex min-h-14 cursor-pointer list-none items-center gap-x-3 px-padx py-2 [&::-webkit-details-marker]:hidden">
        <span className="text-body font-semibold text-ink">Data</span>
        <span className="text-label text-ink2">the exact numbers behind these charts</span>
        <svg
          width="12"
          height="12"
          viewBox="0 0 12 12"
          aria-hidden="true"
          className="ml-auto shrink-0 transition-transform duration-150 group-open:rotate-90"
        >
          <polyline
            points="3,1 9,6 3,11"
            fill="none"
            strokeWidth="1.6"
            strokeLinecap="round"
            strokeLinejoin="round"
            className="stroke-ink2"
          />
        </svg>
      </summary>
      <div className="space-y-8 px-padx pt-2 pb-6">
        <p className="flex items-center gap-2 text-label text-ink2">
          <svg width="10" height="10" viewBox="0 0 10 10" aria-hidden="true">
            <polygon points="5,0.5 0,9.5 10,9.5" className="fill-stop" />
          </svg>
          red = above {whole(bar)}, auto-merge off
        </p>
        <DataTable<PR>
          title="Per PR, newest first"
          rows={rewrite.prs}
          rowKey={(pr) => prKey(pr.repo, pr.pr)}
          columns={[
            { head: "PR", render: (pr) => <PRNumber pr={pr} showRepo={manyRepos} /> },
            { head: "Title", render: (pr) => pr.title, long: true },
            { head: "Run", render: (pr) => pr.run, figure: true },
            { head: "Size", render: (pr) => pr.size, figure: true },
            { head: "Agent lines", render: (pr) => count(pr.agent_lines), figure: true },
            { head: "Owner lines", render: (pr) => count(pr.owner_lines), figure: true },
            {
              head: "Rate",
              render: (pr) => (
                <Rate value={pr.rate_value} text={pr.rate} words={pr.rate} bar={bar} />
              ),
              figure: true,
            },
          ]}
        />
        <DataTable
          title="Trailing 10 after each merged PR, newest first, UTC dates"
          rows={[...rewrite.series].reverse()}
          rowKey={(point) => prKey(point.repo, point.pr)}
          columns={[
            {
              head: "After PR",
              render: (point) => (
                <>
                  #{point.pr}
                  {pointRepos > 1 && (
                    <span className="block font-mono text-micro text-ink2">{point.repo}</span>
                  )}
                </>
              ),
              figure: true,
            },
            { head: "Date", render: (point) => utcDay(point.merged_at), figure: true },
            {
              head: "Trailing 10",
              render: (point) => (
                <Rate value={point.rate_value} text={point.rate} words={point.rate} bar={bar} />
              ),
              figure: true,
            },
          ]}
        />
        <DataTable<SizeRow>
          title="By size"
          rows={sizes}
          rowKey={(size) => size.size}
          columns={[
            { head: "Size", render: (size) => size.size },
            { head: "PRs", render: (size) => size.prs, figure: true },
            {
              head: "Pooled",
              render: (size) => (
                <Rate value={size.rate_value} text={size.rate} words="no rate" bar={bar} />
              ),
              figure: true,
            },
            { head: "Median PR", render: (size) => spreadText(size.median, size), figure: true },
            { head: "p90 PR", render: (size) => spreadText(size.p90, size), figure: true },
          ]}
        />
        <div>
          <DataTable
            title="Trend arrows, in words"
            rows={tileRows}
            rowKey={(entry) => entry.label}
            columns={[
              {
                head: "Tile",
                render: (entry) =>
                  entry.label === "last PR" && lastPR !== undefined
                    ? `last PR ${lastRepo === null ? "" : `${lastRepo} `}#${lastPR}`
                    : entry.label,
              },
              {
                head: "Now",
                render: (entry) => entry.tile.rate,
                figure: true,
              },
              {
                head: "One PR earlier",
                render: (entry) =>
                  entry.tile.previous_rate_value === null
                    ? "-"
                    : percent(entry.tile.previous_rate_value),
                figure: true,
              },
              {
                head: "Trend arrow, in words",
                long: true,
                render: (entry) =>
                  [
                    trendWords(entry.tile) ?? "no earlier window",
                    entry.label === "last PR"
                      ? `${count(entry.tile.agent_lines)} agent lines`
                      : null,
                  ]
                    .filter((part) => part !== null)
                    .join(", "),
              },
            ]}
          />
          <p className="mt-2 text-label text-ink2">
            Lower is better: up is red, down is green. Two moves the same way, the latest at least
            0.5 points.
          </p>
        </div>
        <DataTable<ModelTableRow>
          title="By model"
          rows={models}
          rowKey={(entry) => `${entry.phase}:${entry.row.model}`}
          columns={[
            { head: "Role", render: (entry) => entry.phase },
            {
              head: "Model",
              render: (entry) => modelCell(entry.phase, entry.row.model),
              long: true,
            },
            ...groupColumns(bar).map((column): Column<ModelTableRow> => ({
              head: column.head,
              figure: column.figure,
              render: (entry) => column.render(entry.row),
            })),
            {
              head: "Review-found",
              render: (entry) => count(entry.row.review_found_lines),
              figure: true,
            },
            {
              head: "Owner-change",
              render: (entry) => count(entry.row.owner_change_lines),
              figure: true,
            },
            {
              head: "Unlabelled",
              render: (entry) => count(entry.row.unlabelled_lines),
              figure: true,
            },
          ]}
        />
        <DataTable
          title="By repository"
          rows={rewrite.by_repo}
          rowKey={(repo) => repo.repo}
          columns={[
            { head: "Repository", render: (repo) => repo.repo },
            ...groupColumns(bar).map((column): Column<(typeof rewrite.by_repo)[number]> => ({
              head: column.head,
              figure: column.figure,
              render: (repo) => column.render(repo.average),
            })),
          ]}
        />
        {notes.map((note) => (
          <p key={note} className="-mt-4 text-label text-ink2">
            {note}
          </p>
        ))}
        <DataTable
          title="Models per PR, newest first"
          rows={rewrite.prs}
          rowKey={(pr) => prKey(pr.repo, pr.pr)}
          columns={[
            {
              head: "PR",
              render: (pr) => (
                <>
                  #{pr.pr}
                  {manyRepos && (
                    <span className="block font-mono text-micro text-ink2">{pr.repo}</span>
                  )}
                </>
              ),
              figure: true,
            },
            { head: "Plan", long: true, render: (pr) => modelCell("plan", pr.plan_model) },
            { head: "Build", long: true, render: (pr) => modelCell("build", pr.build_model) },
            { head: "Review", long: true, render: (pr) => modelCell("review", pr.review_model) },
          ]}
        />
      </div>
    </details>
  );
}
