import { useId, useState, type ReactNode } from "react";
import { parsePull } from "../inbox/run";
import { usePageSize } from "../page-size";
import { pageWindow } from "../page-window";
import { Pager } from "../pager";
import { sydneyDateTime } from "../sydney-date-time";
import { prKey } from "./axis";
import {
  NO_RATE,
  UNKNOWN_MODEL,
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
  whole,
} from "./format";
import { PHASES } from "./types";
import type { Group, ModelRow, Phase, PR, RewriteView, SizeRow } from "./types";

// The house card, drawn 10px wider each side than the page's text on a phone, with 16px inside.
const CARD = "card-frame py-4 max-sm:-mx-2.5 max-sm:px-4";

const DEFINITIONS =
  "Rate is the rewrite rate: owner lines as a share of agent lines. Pooled is that rate over all of a group's lines; median PR is the middle PR's own rate; p90 PR is the rate nine PRs in ten stay at or under.";
const NO_PRS = "No PR is measured yet.";

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

function PRNumber({ pr }: { pr: PR }) {
  if (parsePull(pr.pr_url) === null) {
    return <span className="font-mono font-semibold">#{pr.pr}</span>;
  }
  return (
    <a
      href={pr.pr_url}
      target="_blank"
      rel="noopener noreferrer"
      className="inline-flex min-h-11 min-w-11 items-center font-mono font-semibold text-accent underline"
    >
      #{pr.pr}
      <span className="sr-only"> (opens in a new tab)</span>
    </a>
  );
}

function modelCell(phase: Phase, id: string): ReactNode {
  return (
    <>
      <span>{phaseWord(phase, id)}</span>
      {modelName(id) !== id && (
        <span className="block font-mono text-micro wrap-anywhere text-ink2">{id}</span>
      )}
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
  // Too wide for half a stacked row, so below 768 it takes the whole line.
  long?: boolean;
};

// MergedAt is a PR's merge on Sydney's clock, held to one line, or a dash where the text is no
// instant.
function MergedAt({ at }: { at: string }) {
  const text = sydneyDateTime(at);
  if (text === null) {
    return "-";
  }
  return (
    <time dateTime={at} className="font-mono whitespace-nowrap">
      {text}
    </time>
  );
}

// prFirstLine is a PR's first line: the record's numbers, newest first.
function prFirstLine(bar: number): PRField[] {
  return [
    {
      head: "Date Finished (AEST/AEDT)",
      render: (pr) => <MergedAt at={pr.merged_at} />,
      long: true,
    },
    { head: "Run", render: (pr) => <span className="font-mono whitespace-nowrap">{pr.run}</span> },
    { head: "Size", render: (pr) => <span className="font-mono">{pr.size}</span> },
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

const PR_MODELS: { phase: Phase; head: string; of: (pr: PR) => string }[] = [
  { phase: "plan", head: "Plan", of: (pr) => pr.plan_model },
  { phase: "build", head: "Build", of: (pr) => pr.build_model },
  { phase: "review", head: "Review", of: (pr) => pr.review_model },
];

// PRModels names the model of each phase a PR ran, one to a line. A phase that did not run reads
// dim; a build with no single model reads in ink.
function PRModels({ pr }: { pr: PR }) {
  return (
    <dl>
      {PR_MODELS.map(({ phase, head, of }) => {
        const model = of(pr);
        return (
          <div
            key={phase}
            className="flex gap-x-2 text-cap leading-4 max-md:justify-between max-md:text-body max-md:leading-[26px]"
          >
            <dt className="w-[46px] shrink-0 text-ink2 max-md:w-auto max-md:text-cap">{head}</dt>
            <dd className={model === UNKNOWN_MODEL && phase !== "build" ? "text-ink2" : undefined}>
              {phaseWord(phase, model)}
            </dd>
          </div>
        );
      })}
    </dl>
  );
}

// The per-PR table's cells. From 768 a row is two lines: the figures, then the title beside the
// models, the headers on one line. Below 768 the row is a grid of two columns, the number and
// title over the figures as label-and-value pairs, then the models.
const FIRST_CELL =
  "md:pr-4 md:align-baseline md:last:pr-0 max-md:flex max-md:items-baseline max-md:justify-between max-md:gap-x-2 max-md:text-body max-md:leading-[26px] max-md:before:text-cap max-md:before:text-ink2 max-md:before:content-[attr(data-label)]";
const TITLE_CELL =
  "wrap-anywhere md:pr-4 md:pb-2 md:align-top md:leading-[17px] max-md:-order-1 max-md:col-span-2 max-md:text-cap max-md:leading-[18px] max-md:text-ink2";
const MODELS_CELL = "md:pb-2 md:align-top max-md:order-1 max-md:col-span-2";
const HEAD = "font-semibold md:pr-4 md:whitespace-nowrap md:last:pr-0";

type PerPRTableProps = {
  prs: PR[];
  bar: number;
  // Whether a row's title line starts with its repository, which a list spanning more than one
  // needs, since a PR's number names it only within its repository.
  showRepo: boolean;
  // The id of the caption above the table, which names it.
  captionId: string;
};

// PerPRTable is the one table of every measured PR, newest first: its first line is the
// record's numbers and its second the title and the models that ran it, so twelve figures fit
// the page's width. Below 768 each cell names itself and the record stacks into pairs.
function PerPRTable({ prs, bar, showRepo, captionId }: PerPRTableProps) {
  const first = prFirstLine(bar);
  return (
    <table aria-labelledby={captionId} className="mt-2 w-full text-label max-md:block">
      <thead className="max-md:sr-only">
        <tr className="text-left text-cap text-ink2">
          <th scope="col" className={`${HEAD} pt-2 pb-1`}>
            PR
          </th>
          {first.map((field) => (
            <th
              key={field.head}
              scope="col"
              className={`${HEAD} pt-2 pb-1 ${field.figure ? "text-right" : ""}`}
            >
              {field.head}
            </th>
          ))}
        </tr>
        <tr className="border-b border-hair text-left text-cap text-ink2">
          <th scope="col" colSpan={5} className={`${HEAD} pt-1 pb-2`}>
            Title
          </th>
          <th scope="col" colSpan={3} className={`${HEAD} pt-1 pb-2`}>
            Plan, build, review
          </th>
        </tr>
      </thead>
      {prs.map((pr) => (
        <tbody
          key={prKey(pr.repo, pr.pr)}
          className="border-b border-hair max-md:grid max-md:grid-cols-2 max-md:gap-x-4 max-md:pb-4 max-md:first-of-type:border-t"
        >
          <tr className="max-md:contents">
            <th
              scope="row"
              className="text-left font-normal md:pr-4 md:align-baseline max-md:-order-2 max-md:col-span-2 max-md:text-body"
            >
              <PRNumber pr={pr} />
            </th>
            {first.map((field) => (
              <td
                key={field.head}
                data-label={field.head}
                className={`${FIRST_CELL} ${field.figure ? "md:text-right" : ""} ${field.long ? "max-md:col-span-2" : ""}`}
              >
                {field.render(pr)}
              </td>
            ))}
          </tr>
          <tr className="max-md:contents">
            <td colSpan={5} className={TITLE_CELL}>
              {showRepo && (
                <span className="mr-2 font-mono text-micro whitespace-nowrap text-ink2">
                  {pr.repo}
                </span>
              )}
              {pr.title}
            </td>
            <td colSpan={3} className={MODELS_CELL}>
              <PRModels pr={pr} />
            </td>
          </tr>
        </tbody>
      ))}
    </table>
  );
}

const PR_NOTES = [
  "Trailing 10 is the rewrite rate over the ten merged PRs ending at that one, the trend chart's numbers; it starts at the tenth measured PR, so the oldest nine PRs show none.",
  "Plan, build and review are the strip's lanes in words.",
];

type PagedPRsProps = { prs: PR[]; bar: number; showRepo: boolean };

// PagedPRs is the per-PR table's card: its caption, the pager, the notes and the rows, with a
// second pager 16px under the last row on a phone.
function PagedPRs({ prs, bar, showRepo }: PagedPRsProps) {
  const pageSize = usePageSize();
  const [page, setPage] = useState(0);
  const captionId = useId();
  // Five a page below 768 leaves fewer pages than ten, so a page kept across a width change can
  // sit past the last one: the window clamps it.
  const range = pageWindow(prs.length, page, pageSize);
  const rows = prs.slice(range.page * pageSize, range.page * pageSize + pageSize);
  return (
    <div className={CARD}>
      <p id={captionId} className="text-cap font-semibold text-ink2">
        Per PR, newest first
      </p>
      {range.paged && (
        <div className="mt-2">
          <Pager
            noun="PRs"
            total={prs.length}
            page={range.page}
            pageSize={pageSize}
            onPage={setPage}
          />
        </div>
      )}
      {PR_NOTES.map((note, i) => (
        <p
          key={note}
          className={`text-label leading-[18px] text-ink2 ${i === 0 ? "mt-4" : "mt-1.5"}`}
        >
          {note}
        </p>
      ))}
      <PerPRTable prs={rows} bar={bar} showRepo={showRepo} captionId={captionId} />
      {range.paged && (
        <div className="mt-4 md:hidden">
          <Pager
            noun="PRs"
            total={prs.length}
            page={range.page}
            pageSize={pageSize}
            onPage={setPage}
            announce={false}
          />
        </div>
      )}
    </div>
  );
}

type DataSectionProps = {
  rewrite: RewriteView;
  // Where the section sits on its page, which owns its place and the gaps around it.
  className?: string;
};

// DataSection holds the exact numbers behind every chart, which are the charts' text
// alternative: the three windows in words and the splits by size, model and repository in one
// card, then the per-PR table, paged, in another. A rate above the bar is red with a triangle, and
// the legend says what red means. With no measured PR it says so in one card.
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
  const measured = rewrite.prs.length > 0;
  return (
    <section aria-labelledby="data-title" className={className}>
      <h2 id="data-title" className="text-head font-semibold text-ink">
        Data
      </h2>
      <p className="mt-1 text-body text-ink2">The exact numbers behind these charts.</p>
      {measured && (
        <>
          <p className="mt-2 text-label text-ink2">{DEFINITIONS}</p>
          <p className="mt-2 flex items-center gap-2 text-label text-ink2">
            <svg width="10" height="10" viewBox="0 0 10 10" aria-hidden="true">
              <polygon points="5,0.5 0,9.5 10,9.5" className="fill-stop" />
            </svg>
            red = above {whole(bar)}, auto-merge off
          </p>
        </>
      )}
      {!measured && (
        <div className={`mt-4 ${CARD}`}>
          <p className="text-body text-ink2">{NO_PRS}</p>
        </div>
      )}
      {measured && (
        <div className="mt-4 space-y-5 md:space-y-6">
          <div className={`${CARD} space-y-6`}>
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
                Lower is better: up is red, down is green. Two moves the same way, the latest at
                least 0.5 points.
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
                {
                  head: "Median PR",
                  render: (size) => spreadText(size.median, size),
                  figure: true,
                },
                { head: "p90 PR", render: (size) => spreadText(size.p90, size), figure: true },
              ]}
            />
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
              <p key={note} className="text-label text-ink2">
                {note}
              </p>
            ))}
          </div>
          <PagedPRs prs={rewrite.prs} bar={bar} showRepo={manyRepos} />
        </div>
      )}
    </section>
  );
}
