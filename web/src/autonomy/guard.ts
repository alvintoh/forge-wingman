import type {
  AutonomyView,
  Group,
  MergeState,
  ModelRow,
  PR,
  Point,
  RepoRow,
  RewriteView,
  SizeRow,
  Tile,
} from "./types";

type Check = (value: unknown) => boolean;

const isNumber: Check = (value) => typeof value === "number";
const isString: Check = (value) => typeof value === "string";
const isBoolean: Check = (value) => typeof value === "boolean";
const isTimestamp: Check = (value) => typeof value === "string" && !Number.isNaN(Date.parse(value));
const isDirection: Check = (value) => value === "up" || value === "down" || value === "flat";

function nullable(check: Check): Check {
  return (value) => value === null || check(value);
}

function listOf(check: Check): Check {
  return (value) => Array.isArray(value) && value.every(check);
}

// shape checks a document field by field: each key the type names must be there
// and hold what the check says, and the type's own keys are what the checks are
// keyed by, so a field added to a type is a compile error here until it is read.
function shape<T>(checks: { [K in keyof T]-?: Check }): (value: unknown) => value is T {
  const entries = Object.entries<Check>(checks);
  return (value): value is T =>
    typeof value === "object" &&
    value !== null &&
    entries.every(([key, check]) => key in value && check(Reflect.get(value, key)));
}

const isMergeState = shape<MergeState>({
  switch_on: isBoolean,
  rate_allows: isBoolean,
  rate_value: isString,
  limit: isString,
  window: isNumber,
  effective: isBoolean,
  reason: isString,
});

const isTile = shape<Tile>({
  prs: listOf(isNumber),
  asked: isNumber,
  agent_lines: isNumber,
  owner_lines: isNumber,
  rate: isString,
  rate_value: nullable(isNumber),
  previous_rate_value: nullable(isNumber),
  trend: isDirection,
});

const isPoint = shape<Point>({
  pr: isNumber,
  merged_at: isTimestamp,
  rate: isString,
  rate_value: nullable(isNumber),
});

const isPR = shape<PR>({
  pr: isNumber,
  run: isString,
  repo: isString,
  size: isString,
  title: isString,
  agent_lines: isNumber,
  owner_lines: isNumber,
  rate: isString,
  rate_value: nullable(isNumber),
  merged_at: isTimestamp,
  pr_url: isString,
  plan_model: isString,
  build_model: isString,
  review_model: isString,
});

const groupChecks: { [K in keyof Group]-?: Check } = {
  prs: isNumber,
  rate: nullable(isString),
  rate_value: nullable(isNumber),
  median: nullable(isString),
  median_value: nullable(isNumber),
  p90: nullable(isString),
  p90_value: nullable(isNumber),
  agent_lines: isNumber,
  owner_lines: isNumber,
  review_found_lines: isNumber,
  owner_change_lines: isNumber,
  unlabelled_lines: isNumber,
  no_agent_lines_prs: isNumber,
  no_agent_owner_lines: isNumber,
};

const isGroup = shape<Group>(groupChecks);

const isSizeRow = shape<SizeRow>({ ...groupChecks, size: isString });

const isModelRow = shape<ModelRow>({ ...groupChecks, model: isString });

const isRepoRow = shape<RepoRow>({
  repo: isString,
  tiles: listOf(isTile),
  series: listOf(isPoint),
  average: isGroup,
});

const isBars = shape<RewriteView["bars"]>({ rollback: isNumber });

const isByModel = shape<RewriteView["by_model"]>({
  plan: listOf(isModelRow),
  build: listOf(isModelRow),
  review: listOf(isModelRow),
});

const isRewrite = shape<RewriteView>({
  tiles: listOf(isTile),
  series: listOf(isPoint),
  prs: listOf(isPR),
  by_size: listOf(isSizeRow),
  by_model: isByModel,
  by_repo: listOf(isRepoRow),
  bars: isBars,
});

// isAutonomy narrows the endpoint's answer to the whole of what the page reads:
// the auto-merge state and every figure the rewrite charts draw. It is strict, so
// an older endpoint, which serves no such figures, reads as a document of another
// shape and shows the page's error state.
export const isAutonomy = shape<AutonomyView>({
  auto_merge: isMergeState,
  rewrite: isRewrite,
});
