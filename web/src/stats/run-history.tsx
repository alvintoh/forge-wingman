import { captionClass } from "../caption";
import { sameRepository } from "../project-search";
import { useRead } from "./use-read";

type HistoryRun = {
  run: string;
  ticket: string;
  repo: string;
  kind: string;
  words: string;
};

// The run history endpoint's own shape: the settled runs oldest first, each
// with its repository, the kind it is drawn as and the words it is read as.
type HistoryResponse = {
  runs: HistoryRun[];
};

// The fill of each kind of run, from the tokens the inbox already draws its
// reasons with. Some kinds share a colour, so the words carry which one a bar is.
const kindFill: Record<string, string> = {
  allowance: "bg-defer",
  cancelled: "bg-muted",
  credential: "bg-stop",
  decision: "bg-stop",
  draft: "bg-defer",
  failed: "bg-stop",
  merged: "bg-ok",
  "no-changes": "bg-muted",
  other: "bg-muted",
  "pr-open": "bg-review",
};

const OTHER_FILL = "bg-muted";

// RunHistory draws the latest settled runs as one bar each, oldest to newest,
// with the merged count, narrowed to the project the page is on; each bar's
// words are read out, not drawn.
// While it loads, its line keeps the height of the strip (26px), so the ready
// state does not push the page down.
export function RunHistory({ project }: { project: string | null }) {
  const read = useRead("/api/run-history", isHistory, "runHistoryNotRead");
  const served = read.status === "ready" ? read.body.runs : [];
  // A run that recorded no repository matches no project, so it shows under All alone.
  const runs =
    project === null ? served : served.filter((run) => sameRepository(run.repo, project));
  const merged = runs.filter((run) => run.kind === "merged").length;
  return (
    <section aria-labelledby="run-history-title" className="border-b border-hair pb-5">
      <div className="flex items-baseline justify-between gap-4">
        {/* One line whatever it counts and names, so choosing a project never moves the
            card below. */}
        <h2 id="run-history-title" className={`${captionClass} min-w-0 truncate text-muted`}>
          {title(runs.length, project)}
        </h2>
        {read.status === "ready" && runs.length > 0 && (
          <p className="shrink-0 font-mono text-micro font-semibold text-ok">{merged} merged</p>
        )}
      </div>
      {read.status === "loading" && (
        <p role="status" className="mt-3 min-h-[26px] text-label text-ink2">
          Reading the run history…
        </p>
      )}
      {read.status === "error" && (
        <p role="alert" className="mt-3 min-h-[26px] text-label text-ink2">
          The run history could not be read.
        </p>
      )}
      {read.status === "ready" && served.length === 0 && (
        <p className="mt-3 min-h-[26px] text-label text-ink2">No run has settled yet.</p>
      )}
      {read.status === "ready" && served.length > 0 && runs.length === 0 && project !== null && (
        <p className="mt-3 min-h-[26px] truncate text-label text-ink2">
          No run for {project} in the last {served.length} {served.length === 1 ? "run" : "runs"}.
        </p>
      )}
      {read.status === "ready" && runs.length > 0 && (
        <ol aria-label="The latest settled runs, oldest first" className="mt-3 flex gap-[5px]">
          {runs.map((run) => (
            <li
              key={run.run}
              className={`h-6.5 max-w-4 min-w-0 flex-1 rounded-tag ${kindFill[run.kind] ?? OTHER_FILL}`}
            >
              <span className="sr-only">
                {run.ticket || run.run}: {run.words}
              </span>
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}

// title names the runs drawn, and only when there are some: while the history is
// loading, unread or empty the caption says what the panel is, not a count. A
// narrowed history names the project it counted.
function title(count: number, project: string | null): string {
  if (count === 0) {
    return "Run history";
  }
  const runs = count === 1 ? "Last run" : `Last ${count} runs`;
  return project === null ? runs : `${runs}, ${project}`;
}

// isHistory narrows the endpoint's answer to what this view draws from: a list
// of runs that each name the kind they are drawn as and the words they are read
// as. The merged count is counted from those kinds, so it needs no field.
function isHistory(body: unknown): body is HistoryResponse {
  if (typeof body !== "object" || body === null) {
    return false;
  }
  if (!("runs" in body) || !Array.isArray(body.runs)) {
    return false;
  }
  return body.runs.every(isHistoryRun);
}

function isHistoryRun(run: unknown): run is HistoryRun {
  return (
    typeof run === "object" &&
    run !== null &&
    "run" in run &&
    typeof run.run === "string" &&
    "ticket" in run &&
    typeof run.ticket === "string" &&
    "repo" in run &&
    typeof run.repo === "string" &&
    "kind" in run &&
    typeof run.kind === "string" &&
    "words" in run &&
    typeof run.words === "string"
  );
}
