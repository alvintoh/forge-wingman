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
  const pull = parsePull(run.pr_url);
  return run.title ?? (pull === null ? "Pull request" : `Pull request #${pull.number}`);
}
