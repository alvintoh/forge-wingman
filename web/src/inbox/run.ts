type Checks = {
  state: string;
  summary: string;
};

export type InboxRun = {
  run: string;
  ticket: string;
  repo: string;
  pr_url: string;
  wait_reason: string;
  updated_at: string;
  title?: string;
  head_sha?: string;
  draft?: boolean;
  checks?: Checks;
  stop_reason?: string;
  outcome?: string;
  // The attempt the run is on, from 0; a retry names it so the server can refuse a stale one.
  attempt?: number;
  retryable?: boolean;
  // Set for a run that ended and waits for a retry, whatever pull request its row still names; its
  // title is then the ticket's.
  stopped?: boolean;
};

function isChecks(value: unknown): value is Checks {
  return (
    typeof value === "object" &&
    value !== null &&
    "state" in value &&
    typeof value.state === "string" &&
    "summary" in value &&
    typeof value.summary === "string"
  );
}

function isInboxRun(row: unknown): row is InboxRun {
  if (typeof row !== "object" || row === null) {
    return false;
  }
  const fields = new Map(Object.entries(row));
  const text = (key: string) => typeof fields.get(key) === "string";
  const optional = (key: string, kind: string) =>
    !fields.has(key) || typeof fields.get(key) === kind;
  return (
    ["run", "ticket", "repo", "pr_url", "wait_reason", "updated_at"].every(text) &&
    optional("title", "string") &&
    optional("head_sha", "string") &&
    optional("draft", "boolean") &&
    optional("stop_reason", "string") &&
    optional("outcome", "string") &&
    optional("attempt", "number") &&
    optional("retryable", "boolean") &&
    optional("stopped", "boolean") &&
    (!fields.has("checks") || isChecks(fields.get("checks")))
  );
}

export function isInbox(body: unknown): body is { runs: InboxRun[] } {
  return (
    typeof body === "object" &&
    body !== null &&
    "runs" in body &&
    Array.isArray(body.runs) &&
    body.runs.every(isInboxRun)
  );
}

export function parsePull(prUrl: string): { repository: string; number: string } | null {
  const match = /^https:\/\/github\.com\/([^/]+\/[^/]+)\/pull\/(\d+)\/?(?:[?#].*)?$/.exec(prUrl);
  return match?.[1] !== undefined && match[2] !== undefined
    ? { repository: match[1], number: match[2] }
    : null;
}

export function titleOf(run: InboxRun): string {
  if (run.title !== undefined) {
    return run.title;
  }
  if (isStopped(run)) {
    return run.ticket;
  }
  const pull = parsePull(run.pr_url);
  return pull === null ? "Pull request" : `Pull request #${pull.number}`;
}

// isStopped is a run that ended and waits for a retry, which the inbox lists for that, not for a merge,
// even when its row still names an earlier attempt's draft.
export function isStopped(run: InboxRun): boolean {
  return run.stopped === true;
}

// Why a run stopped, in words, keyed by the runner's stop reason. waiting-on-owner is a run parked on
// a decision the owner never answered.
const stopWords: Record<string, string> = {
  "waiting-on-owner": "Stopped at an unconfirmed decision",
  "agent-timeout": "Stopped, the agent timed out",
  "agent-no-progress": "Stopped, the agent made no progress",
  "allowance-exhausted": "Stopped, the model allowance ran out",
  "credential-absent": "Stopped, a model credential is missing",
  "rebase-conflict": "Stopped, its changes conflict with main",
  "workflow-change": "Stopped, it changed a workflow file",
  "run-budget": "Stopped, the run's time budget was spent",
};

// stopReasonOf words why a run stopped; a reason the table does not know is shown as the runner
// names it, with its hyphens as spaces.
export function stopReasonOf(run: InboxRun): string {
  const reason = run.stop_reason ?? run.outcome ?? "";
  if (reason === "" || reason === "stopped") {
    return "Stopped";
  }
  return stopWords[reason] ?? `Stopped, ${reason.replaceAll("-", " ")}`;
}
