// What makes a run fail: a route the app read with no fixture, a request the
// fixture API does not serve, a shot the checks refused, a route whose page
// logged an error, a quoted string the page never showed, and an element wider
// than its box. One countable line each, and the exit code they demand.

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

/** One quoted string a criterion names that no page in its scope showed. */
export type MissingString = {
  // The route and width the string was looked for at, e.g. "/stats@s".
  state: string;
  string: string;
};

/** One element wider than its box at a width, on a route. */
export type Overflow = {
  route: string;
  width: string;
  element: string;
  scrollWidth: number;
  clientWidth: number;
};

/** The failures the page checks found, beside the shots' own. */
type Checks = {
  missingStrings: readonly MissingString[];
  overflows: readonly Overflow[];
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
  checks: Checks = { missingStrings: [], overflows: [] },
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
  for (const missing of checks.missingStrings) {
    failures.push(
      `shotsFailed reason=missing-string state=${missing.state} ` +
        `string=${JSON.stringify(missing.string)}`,
    );
  }
  for (const overflow of checks.overflows) {
    failures.push(
      `shotsFailed reason=overflow state=${overflow.route}@${overflow.width} ` +
        `element=${JSON.stringify(overflow.element)} ` +
        `scrollWidth=${overflow.scrollWidth} clientWidth=${overflow.clientWidth}`,
    );
  }
  return { failures, exitCode: failures.length > 0 ? 1 : 0 };
}
