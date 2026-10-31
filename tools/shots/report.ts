// What makes a run fail: a route the app read with no fixture, a request the
// fixture API does not serve, a shot the checks refused, and a route whose page
// logged an error. One countable line each, and the exit code they demand.

/** One shot the checks refused: its file-name stem, width key and reason. */
export type Refusal = {
  route: string;
  width: string;
  detail: string;
};

/** One route whose page logged an error while it was captured. */
export type PageError = {
  route: string;
  detail: string;
};

/** A run's failure lines and the exit code they demand. */
export type Outcome = {
  failures: string[];
  exitCode: number;
};

/**
 * outcome words every failure of a run as one line with a fixed leading token,
 * a `reason=` discriminator and named fields, and answers 1 when there is any.
 */
export function outcome(
  missing: ReadonlyMap<string, number>,
  writes: ReadonlyMap<string, number>,
  refusals: readonly Refusal[],
  pageErrors: readonly PageError[],
): Outcome {
  const failures: string[] = [];
  for (const [path, count] of missing) {
    failures.push(`shotsFailed reason=missing-fixture route=${path} count=${count}`);
  }
  for (const [request, count] of writes) {
    failures.push(
      `shotsFailed reason=unexpected-write request=${JSON.stringify(request)} count=${count}`,
    );
  }
  for (const refusal of refusals) {
    failures.push(
      `shotsFailed reason=refused shot=${refusal.route}-${refusal.width} ` +
        `detail=${JSON.stringify(refusal.detail)}`,
    );
  }
  for (const pageError of pageErrors) {
    failures.push(
      `shotsFailed reason=page-error route=${pageError.route} ` +
        `detail=${JSON.stringify(pageError.detail)}`,
    );
  }
  return { failures, exitCode: failures.length > 0 ? 1 : 0 };
}
