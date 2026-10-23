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
// reasons with. Two pairs share a colour, so the words carry which one a bar is.
const kindFill: Record<string, string> = {
  allowance: "bg-defer",
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

const captionClass = "font-mono text-label font-semibold tracking-[1.4px] text-muted uppercase";

// RunHistory draws the latest settled runs as one bar each, oldest to newest,
// with the merged count and, under it, the runs that did not merge in words.
// While it loads, its line keeps the height of the strip and one legend line
// (26px, the 12px gap, 16px), so the ready state does not push the page down.
export function RunHistory() {
  const read = useRead("/api/run-history", isHistory, "runHistoryNotRead");
  const runs = read.status === "ready" ? read.body.runs : [];
  return (
    <section aria-labelledby="run-history-title" className="border-b border-hair pb-5">
      <div className="flex items-baseline justify-between gap-4">
        <h2 id="run-history-title" className={captionClass}>
          {title(runs.length)}
        </h2>
        {read.status === "ready" && runs.length > 0 && (
          <p className="font-mono text-micro font-semibold text-ok">{read.body.merged} merged</p>
        )}
      </div>
      {read.status === "loading" && (
        <p role="status" className="mt-3 min-h-[54px] text-label text-ink2">
          Reading the run history…
        </p>
      )}
      {read.status === "error" && (
        <p role="alert" className="mt-3 text-label text-ink2">
          The run history could not be read.
        </p>
      )}
      {read.status === "ready" && runs.length === 0 && (
        <p className="mt-3 text-label text-ink2">No run has settled yet.</p>
      )}
      {read.status === "ready" && runs.length > 0 && (
        <>
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
          <Legend runs={runs} />
        </>
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

// Legend counts the runs that did not merge by their words, in the order each
// first appears, so a colour two outcomes share is never the only thing said.
function Legend({ runs }: { runs: HistoryRun[] }) {
  const counts = new Map<string, number>();
  for (const run of runs) {
    if (run.kind !== "merged") {
      counts.set(run.words, (counts.get(run.words) ?? 0) + 1);
    }
  }
  if (counts.size === 0) {
    return null;
  }
  const line = [...counts].map(([words, count]) => `${count} ${words}`).join(" · ");
  return <p className="mt-3 text-micro text-muted">{line}</p>;
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
