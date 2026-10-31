import { useState, type ReactNode } from "react";
import { parsePull } from "../inbox/run";
import { usePageSize } from "../page-size";
import { pageWindow } from "../page-window";
import { Pager } from "../pager";
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

// DataTable is one table of the Data section. Its first column heads each row, and
// below the md breakpoint a row stacks, each cell naming itself, so the table needs
// no sideways scroll on a phone.
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

type PRField = {
  head: string;
  render: (pr: PR) => ReactNode;
  // A figure sits right-aligned beside the line's other figures.
  figure?: boolean;
  // How many of the first line's eight columns the cell takes.
  span?: number;
};

// prFirstLine is a PR's first line: the record's numbers, newest first.
function prFirstLine(bar: number): PRField[] {
  return [
    {
      head: "Date (UTC)",
      render: (pr) => <span className="font-mono">{utcDay(pr.merged_at)}</span>,
      figure: true,
    },
    { head: "Run", render: (pr) => <span className="font-mono">{pr.run}</span>, figure: true },
    { head: "Size", render: (pr) => pr.size },
    {
      head: "Agent lines",
      render: (pr) => <span className="font-mono">{count(pr.agent_lines)}</span>,
      figure: true,
    },
    {
      head: "Owner lines",
      render: (pr) => <span className="font-mono">{count(pr.owner_lines)}</span>,
      figure: true,
    },
    {
      head: "Rate",
      render: (pr) => <Rate value={pr.rate_value} text={pr.rate} words={pr.rate} bar={bar} />,
      figure: true,
    },
    {
      head: "Trailing 10",
      render: (pr) =>
        pr.trailing_10 === null ? (
          "-"
        ) : (
          <Rate
            value={pr.trailing_10_value}
            text={pr.trailing_10}
            words={pr.trailing_10}
            bar={bar}
          />
        ),
      figure: true,
    },
  ];
}

// The second line of a PR's row: the title, and the model of each phase that ran.
const PR_SECOND_LINE: PRField[] = [
  { head: "Title", render: (pr) => pr.title, span: 5 },
  { head: "Plan", render: (pr) => modelCell("plan", pr.plan_model) },
  { head: "Build", render: (pr) => modelCell("build", pr.build_model) },
  { head: "Review", render: (pr) => modelCell("review", pr.review_model) },
];

// The per-PR table's cells: below 768 a label drawn from the cell's data-label, and from it a
// figure right-aligned beside the line's other figures.
const FIRST_CELL =
  "pt-2 pr-4 pb-1 max-md:flex max-md:items-baseline max-md:justify-between max-md:gap-x-4 max-md:py-1 max-md:pr-0 max-md:before:text-cap max-md:before:text-ink2 max-md:before:content-[attr(data-label)]";
const SECOND_CELL =
  "pt-1 pr-4 pb-2 max-md:block max-md:py-1 max-md:pr-0 max-md:before:block max-md:before:text-cap max-md:before:text-ink2 max-md:before:content-[attr(data-label)]";

type PerPRTableProps = {
  prs: PR[];
  bar: number;
  // Whether a row shows its repository, which a list spanning more than one needs.
  showRepo: boolean;
};

// PerPRTable is the one table of every measured PR, newest first: its first line is the
// record's numbers and its second the title and the models that ran it, so twelve figures fit
// the page's width. Below 768 each cell names itself and the record stacks into pairs.
function PerPRTable({ prs, bar, showRepo }: PerPRTableProps) {
  const first = prFirstLine(bar);
  // The 5px at the left keeps a link's 3px focus ring and its 2px offset inside the scroller.
  return (
    <div className="-ml-[5px] mt-4 overflow-x-auto pl-[5px]">
      <table className="w-full text-label max-md:block">
        <caption className="pb-1 text-left text-cap font-semibold text-ink2 max-md:block">
          Per PR, newest first
        </caption>
        <thead className="max-md:sr-only">
          <tr className="text-left text-cap text-ink2">
            <th scope="col" className="pt-2 pr-4 pb-1 font-semibold">
              PR
            </th>
            {first.map((field) => (
              <th
                key={field.head}
                scope="col"
                className={`pt-2 pr-4 pb-1 font-semibold ${field.figure ? "text-right" : ""}`}
              >
                {field.head}
              </th>
            ))}
          </tr>
          <tr className="border-b border-hair text-left text-cap text-ink2">
            <th scope="col" colSpan={5} className="pt-1 pr-4 pb-2 font-semibold">
              Title
            </th>
            <th scope="col" className="pt-1 pr-4 pb-2 font-semibold">
              Plan
            </th>
            <th scope="col" className="pt-1 pr-4 pb-2 font-semibold">
              Build
            </th>
            <th scope="col" className="pt-1 pr-4 pb-2 font-semibold">
              Review
            </th>
          </tr>
        </thead>
        {prs.map((pr) => (
          <tbody key={prKey(pr.repo, pr.pr)} className="max-md:block">
            <tr className="max-md:block">
              <th
                scope="row"
                className="pt-2 pr-4 pb-1 text-left font-normal max-md:block max-md:py-1 max-md:pr-0"
              >
                <PRNumber pr={pr} showRepo={showRepo} />
              </th>
              {first.map((field) => (
                <td
                  key={field.head}
                  data-label={field.head}
                  className={`${FIRST_CELL} ${field.figure ? "md:text-right" : ""}`}
                >
                  {field.render(pr)}
                </td>
              ))}
            </tr>
            <tr className="border-b border-hair max-md:block">
              {PR_SECOND_LINE.map((field) => (
                <td
                  key={field.head}
                  colSpan={field.span}
                  data-label={field.head}
                  className={SECOND_CELL}
                >
                  {field.render(pr)}
                </td>
              ))}
            </tr>
          </tbody>
        ))}
      </table>
    </div>
  );
}

type PagedPRsProps = { prs: PR[]; bar: number; showRepo: boolean };

// Why a PR shows a dash under trailing 10: fewer than nine merges came before it.
const TRAILING_NOTE =
  "The trailing 10 is the rate pooled over a PR and the nine merged before it: the oldest nine show a dash, and every PR does until ten have merged.";

// PagedPRs is the per-PR table with its pager: the range and the buttons above the rows, a
// second pager 16px under the last row on a phone, and the note that explains the dash.
function PagedPRs({ prs, bar, showRepo }: PagedPRsProps) {
  const pageSize = usePageSize();
  const [page, setPage] = useState(0);
  // Five a page below 768 leaves fewer pages than ten, so a page kept across a width change can
  // sit past the last one: the window clamps it.
  const range = pageWindow(prs.length, page, pageSize);
  const rows = prs.slice(range.page * pageSize, range.page * pageSize + pageSize);
  const pager = (
    <Pager noun="PRs" total={prs.length} page={range.page} pageSize={pageSize} onPage={setPage} />
  );
  return (
    <div>
      {pager}
      <p className="mt-2 text-label text-ink2">{TRAILING_NOTE}</p>
      <PerPRTable prs={rows} bar={bar} showRepo={showRepo} />
      {range.paged && <div className="mt-4 md:hidden">{pager}</div>}
    </div>
  );
}

type DataSectionProps = {
  rewrite: RewriteView;
  // Where the section sits on its page, which owns its place and the gaps around it.
  className?: string;
};

// DataSection holds the exact numbers behind every chart, which are the charts' text
// alternative: the three windows in words and the splits by size, model and repository, then
// one table of every measured PR, paged. A rate above the bar is red with a triangle, and the
// legend says what red means. It draws nothing with no measured PR, where the section above
// says why there are none.
export function DataSection({ rewrite, className }: DataSectionProps) {
  const bar = rewrite.bars.rollback;
  const sizes = rewrite.by_size.filter((size) => size.prs > 0);
  const models: ModelTableRow[] = PHASES.flatMap((phase) =>
    rewrite.by_model[phase].map((row) => ({ phase, row })),
  );
  const tileRows = rewrite.tiles.map((tile, i) => ({ tile, label: TILE_ROWS[i] ?? "" }));
  const lastPR = rewrite.tiles[2]?.prs[0];
  const lastRepo = lastPR === undefined ? null : lastPrRepo(rewrite.prs, lastPR);
  const notes = noAgentNotes(rewrite);
  // A PR's number names it only within its repository, so rows show the repository where the
  // lists span more than one.
  const manyRepos = new Set(rewrite.prs.map((pr) => pr.repo)).size > 1;
  return (
    <section aria-labelledby="data-title" className={className}>
      <h2 id="data-title" className="text-head font-semibold text-ink">
        Data
      </h2>
      <p className="mt-1 text-body text-ink2">The exact numbers behind these charts.</p>
      <p className="mt-2 flex items-center gap-2 text-label text-ink2">
        <svg width="10" height="10" viewBox="0 0 10 10" aria-hidden="true">
          <polygon points="5,0.5 0,9.5 10,9.5" className="fill-stop" />
        </svg>
        red = above {whole(bar)}, auto-merge off
      </p>
      {rewrite.prs.length > 0 && (
        <div className="mt-4 space-y-8">
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
          <DataTable<ModelTableRow>
            title="By model, by role"
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
          <PagedPRs prs={rewrite.prs} bar={bar} showRepo={manyRepos} />
        </div>
      )}
    </section>
  );
}
