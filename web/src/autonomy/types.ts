// MergeState is the auto-merge state the endpoint serves: the owner's switch
// beside the rate's verdict over the trailing window, the bar it is compared to
// and the reason the pair gives.
export type MergeState = {
  switch_on: boolean;
  rate_allows: boolean;
  rate_value: string;
  limit: string;
  // Window is how many trailing merged PRs the rate covers.
  window: number;
  effective: boolean;
  // Reason is why auto-merge is or is not effective: one of the endpoint's own
  // stable tokens, which reasonText words.
  reason: string;
};

export type TrendDirection = "up" | "down" | "flat";

// Tile is one trailing window: the PRs it covers, how many it asked for (a short
// history may not reach it), its pooled rate and the rate one PR earlier. The
// endpoint serves a previous rate only for a full window, and the trend only
// beside one. Rates are fractions: 0.122 is 12.2%.
export type Tile = {
  prs: number[];
  asked: number;
  agent_lines: number;
  owner_lines: number;
  rate: string;
  rate_value: number | null;
  previous_rate_value: number | null;
  trend: TrendDirection;
};

// Point is one point of the rolling series: the merge its window ends at. A
// window with no agent lines has no rate to plot.
export type Point = {
  pr: number;
  merged_at: string;
  rate: string;
  rate_value: number | null;
};

// PR is one merged runner PR with its own rate and the model of each phase, or
// "unknown" for a phase that ran none or more than one.
export type PR = {
  pr: number;
  run: string;
  repo: string;
  size: string;
  title: string;
  agent_lines: number;
  owner_lines: number;
  rate: string;
  rate_value: number | null;
  merged_at: string;
  pr_url: string;
  plan_model: string;
  build_model: string;
  review_model: string;
};

// Group is the figures of one group of PRs: a size, a model or a repository's
// average. The rate is null for no PRs, the median and p90 below ten PRs.
export type Group = {
  prs: number;
  rate: string | null;
  rate_value: number | null;
  median: string | null;
  median_value: number | null;
  p90: string | null;
  p90_value: number | null;
  agent_lines: number;
  owner_lines: number;
  review_found_lines: number;
  owner_change_lines: number;
  unlabelled_lines: number;
  no_agent_lines_prs: number;
  no_agent_owner_lines: number;
};

export type SizeRow = Group & { size: string };

export type ModelRow = Group & { model: string };

export type Phase = "plan" | "build" | "review";

export type RepoRow = {
  repo: string;
  tiles: Tile[];
  series: Point[];
  average: Group;
};

export type RewriteView = {
  tiles: Tile[];
  series: Point[];
  prs: PR[];
  by_size: SizeRow[];
  by_model: Record<Phase, ModelRow[]>;
  by_repo: RepoRow[];
  bars: { rollback: number };
};

// AutonomyView is what the autonomy page reads from the endpoint: the
// auto-merge state over the measured merges, and the rewrite figures behind it.
export type AutonomyView = {
  auto_merge: MergeState;
  rewrite: RewriteView;
};
