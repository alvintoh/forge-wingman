import { captionClass } from "../caption";
import { useRead } from "./use-read";

type HistoryRun = {
  run: string;
  ticket: string;
  kind: string;
  words: string;
};

// The run history endpoint's own shape: the settled runs oldest first, each
// with the kind it is drawn as and the words it is read as, and how many merged.
type HistoryResponse = {
  runs: HistoryRun[];
  merged: number;
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
// with the merged count; each bar's words are read out, not drawn.
// While it loads, its line keeps the height of the strip (26px), so the ready
// state does not push the page down.
export function RunHistory() {
  const read = useRead("/api/run-history", isHistory, "runHistoryNotRead");
  const runs = read.status === "ready" ? read.body.runs : [];
  return (
    <section aria-labelledby="run-history-title" className="border-b border-hair pb-5">
      <div className="flex items-baseline justify-between gap-4">
        <h2 id="run-history-title" className={`${captionClass} text-muted`}>
          {title(runs.length)}
        </h2>
        {read.status === "ready" && runs.length > 0 && (
          <p className="font-mono text-micro font-semibold text-ok">{read.body.merged} merged</p>
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
      {read.status === "ready" && runs.length === 0 && (
        <p className="mt-3 min-h-[26px] text-label text-ink2">No run has settled yet.</p>
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
// loading, unread or empty the caption says what the panel is, not a count.
function title(count: number): string {
  if (count === 0) {
    return "Run history";
  }
  return count === 1 ? "Last run" : `Last ${count} runs`;
}

// isHistory narrows the endpoint's answer to what this view draws from: a list
// of runs that each name the words they are read as, and a merged count.
function isHistory(body: unknown): body is HistoryResponse {
  if (typeof body !== "object" || body === null) {
    return false;
  }
  if (!("runs" in body) || !Array.isArray(body.runs)) {
    return false;
  }
  if (!("merged" in body) || typeof body.merged !== "number") {
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
    "kind" in run &&
    typeof run.kind === "string" &&
    "words" in run &&
    typeof run.words === "string"
  );
}
