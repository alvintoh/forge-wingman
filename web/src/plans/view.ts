import { isBoolean, isNumber, isString, listOf, nullable, shape } from "../autonomy/guard";
import type { Read } from "../stats/use-read";

/** The tokens one request of a stage sends fresh, reads from cache and writes out. */
export type Tokens = { fresh: number; cached: number; output: number };

// A stage's typical request, or null when too few runs recorded one.
type StageShape = { stage: string; runs: number; shape: Tokens | null };

/** The word the endpoint writes for a value no record confirms, in place of the value. */
export const UNCONFIRMED = "unconfirmed";

/** A comparison value: the value, or the word UNCONFIRMED where nothing confirms it. */
export type Field<T> = T | typeof UNCONFIRMED;

/** How firm a requests-per-$1 figure is: from the plan's own rate card, or with a published approximation in it. */
export type Mark = "exact" | "estimated";

/** The unattended-use verdicts the endpoint states; any other is unconfirmed. */
export type Verdict = "allowed" | "restricted";

// A model a plan prices, with its uncached input, output and cache-read rates in US dollars per
// million tokens, and the multiplier of its peak-hour price where it has one.
export type PricedModel = {
  model: string;
  input_usd_per_mtok: number;
  output_usd_per_mtok: number;
  cache_read_usd_per_mtok: number;
  peak: { multiplier: number } | null;
};

// How many requests a figure buys at off-peak rates and, where the model prices by the hour, at peak.
export type RequestCounts = { off_peak: number; has_peak: boolean; peak: number };

// requests-per-$1 for the stated model and stage, with the request it was computed for beside it.
export type Figure = RequestCounts & { shape: Tokens & { model: string }; mark: Mark };

// One allowance window in dollars; requests is null when it cannot be converted.
export type Ceiling = { limit_micros: number; requests: RequestCounts | null };

export type Harness = {
  harness: string;
  headless: Field<boolean>;
  structured_output: Field<boolean>;
  denies_planning_edits: Field<boolean>;
};

/** One plan of the endpoint's answer: the comparison's fields, each its value or UNCONFIRMED. */
export type Plan = {
  plan: string;
  models: PricedModel[];
  monthly_price_micros: Field<number>;
  requests_per_dollar: Field<Figure>;
  five_hour: Field<Ceiling>;
  weekly: Field<Ceiling>;
  monthly: Field<Ceiling>;
  strongest: Field<{ model: string }>;
  strongest_cap_micros: Field<number>;
  features: string[];
  harnesses: Harness[];
  verdict: Field<Verdict>;
};

/** What GET /api/plans answers that the screen reads: the plans, each stage's request, and the figure's model and stage. */
export type PlansView = {
  plans: Plan[];
  shapes: StageShape[];
  model: string | null;
  stage: string | null;
};

// A money amount in micros: a whole, non-negative count, so no float error reaches a figure.
const isMicros = (value: unknown) =>
  typeof value === "number" && Number.isSafeInteger(value) && value >= 0;

// A field is its value, checked by `check`, or the word UNCONFIRMED.
function field(check: (value: unknown) => boolean) {
  return (value: unknown) => value === UNCONFIRMED || check(value);
}

const isPricedModel = shape<PricedModel>({
  model: isString,
  input_usd_per_mtok: isNumber,
  output_usd_per_mtok: isNumber,
  cache_read_usd_per_mtok: isNumber,
  peak: nullable(shape<{ multiplier: number }>({ multiplier: isNumber })),
});

const isCounts = shape<RequestCounts>({
  off_peak: isNumber,
  has_peak: isBoolean,
  peak: isNumber,
});

const isMark = (value: unknown) => value === "exact" || value === "estimated";

const isFigure = shape<Figure>({
  off_peak: isNumber,
  has_peak: isBoolean,
  peak: isNumber,
  shape: shape<Figure["shape"]>({
    model: isString,
    fresh: isNumber,
    cached: isNumber,
    output: isNumber,
  }),
  mark: isMark,
});

const isCeiling = shape<Ceiling>({ limit_micros: isMicros, requests: nullable(isCounts) });

const isHarness = shape<Harness>({
  harness: isString,
  headless: field(isBoolean),
  structured_output: field(isBoolean),
  denies_planning_edits: field(isBoolean),
});

const isPlan = shape<Plan>({
  plan: isString,
  models: listOf(isPricedModel),
  monthly_price_micros: field(isMicros),
  requests_per_dollar: field(isFigure),
  five_hour: field(isCeiling),
  weekly: field(isCeiling),
  monthly: field(isCeiling),
  strongest: field(shape<{ model: string }>({ model: isString })),
  strongest_cap_micros: field(isMicros),
  features: listOf(isString),
  harnesses: listOf(isHarness),
  verdict: field((value) => value === "allowed" || value === "restricted"),
});

const isTokens = shape<Tokens>({ fresh: isNumber, cached: isNumber, output: isNumber });

const isStageShape = shape<StageShape>({
  stage: isString,
  runs: isNumber,
  shape: nullable(isTokens),
});

export const isPlansView = shape<PlansView>({
  plans: listOf(isPlan),
  shapes: listOf(isStageShape),
  model: nullable(isString),
  stage: nullable(isString),
});

/** The endpoint's route, which answers the plans alone and, given a model and a stage, the figures. */
export const PLANS_URL = "/api/plans";

/** The model and the stage a figure is priced at. */
export type Choice = { model: string; stage: string };

/** What the owner has chosen so far: either part is unchosen until they touch its control. */
export type Picked = { model?: string; stage?: string };

/** A stage that has a recorded request, so the owner can price it. */
export type ListedShape = { stage: string; tokens: Tokens };

/** One plan's models for the select: the endpoint's qualified id, and the name shown under the plan's heading. */
export type ModelGroup = { plan: string; models: { id: string; name: string }[] };

/** modelName is a model's id without its plan's prefix, as the plan's own heading shows it. */
export function modelName(plan: string, model: string): string {
  const prefix = `${plan}/`;
  return model.startsWith(prefix) ? model.slice(prefix.length) : model;
}

/**
 * modelGroups lists each plan's models in the order the server sends them, plans first. A model a
 * plan listed before is left out of the later plan, so every id is one option, and a plan left with
 * none is left out.
 */
export function modelGroups(view: PlansView): ModelGroup[] {
  const seen = new Set<string>();
  const groups: ModelGroup[] = [];
  for (const plan of view.plans) {
    const models = [];
    for (const priced of plan.models) {
      if (seen.has(priced.model)) {
        continue;
      }
      seen.add(priced.model);
      models.push({ id: priced.model, name: modelName(plan.plan, priced.model) });
    }
    if (models.length > 0) {
      groups.push({ plan: plan.plan, models });
    }
  }
  return groups;
}

/** modelsOf lists the ids of every model in the select, in its order. */
export function modelsOf(view: PlansView): string[] {
  return modelGroups(view).flatMap((group) => group.models.map((model) => model.id));
}

// The first model some plan prices above $0, so the card does not open on a free tier's $0 rates,
// else the first model.
function defaultModel(view: PlansView, models: readonly string[]): string | undefined {
  const paid = models.find((id) =>
    view.plans.some((plan) =>
      plan.models.some(
        (priced) =>
          priced.model === id && priced.input_usd_per_mtok + priced.output_usd_per_mtok > 0,
      ),
    ),
  );
  return paid ?? models[0];
}

/** listedShapes is the stages with a recorded request, in the server's order; a stage with none is left out. */
export function listedShapes(view: PlansView): ListedShape[] {
  return view.shapes.flatMap((stage) =>
    stage.shape === null ? [] : [{ stage: stage.stage, tokens: stage.shape }],
  );
}

/**
 * resolveChoice is the model and stage the card shows: the owner's own while the catalogue still
 * lists them, else the first model some plan prices above $0 and the first listed stage. It is null while there is no model
 * or no listed stage, so nothing is priced.
 */
export function resolveChoice(view: PlansView, picked: Picked): Choice | null {
  const models = modelsOf(view);
  const stages = listedShapes(view);
  const model = models.find((id) => id === picked.model) ?? defaultModel(view, models);
  const stage = stages.find((listed) => listed.stage === picked.stage) ?? stages[0];
  return model === undefined || stage === undefined ? null : { model, stage: stage.stage };
}

/** figuresUrl is the read that prices the choice, or null while there is nothing to price. */
export function figuresUrl(choice: Choice | null): string | null {
  if (choice === null) {
    return null;
  }
  return `${PLANS_URL}?${new URLSearchParams({ model: choice.model, stage: choice.stage })}`;
}

/** What the figures read holds for the url asked: a read of an earlier url is still loading this one. */
export type FiguresState = "none" | "loading" | "error" | "ready";

export function figuresState(read: Read<PlansView>, url: string | null): FiguresState {
  if (url === null) {
    return "none";
  }
  if (read.status === "loading" || read.url !== url) {
    return "loading";
  }
  return read.status;
}

/**
 * comparedView is the answer the plans side by side read: the figures' own once they are in, since
 * only they carry the requests per $1, the catalogue's while there is nothing to price, and none
 * while the figures are read or could not be (the page shows its skeleton or its error then).
 */
export function comparedView(
  catalogue: Read<PlansView>,
  figures: Read<PlansView>,
  state: FiguresState,
): PlansView | null {
  if (state === "ready" && figures.status === "ready") {
    return figures.body;
  }
  if (state === "none" && catalogue.status === "ready") {
    return catalogue.body;
  }
  return null;
}

/** reloadFor is the read TRY AGAIN runs: the catalogue when it failed, else the figures. */
export function reloadFor(
  catalogue: Read<PlansView> & { reload: () => void },
  figures: { reload: () => void },
): () => void {
  return catalogue.status === "error" ? catalogue.reload : figures.reload;
}

/** count is a whole number with thousands separators. */
export function count(n: number): string {
  return n.toLocaleString("en-US");
}

/** The "<n> fresh · <n> cached · <n> output" line a shape row prints. */
export function tokensLine(tokens: Tokens): string {
  return `${count(tokens.fresh)} fresh · ${count(tokens.cached)} cached · ${count(tokens.output)} output`;
}

/** The name a shape row carries: its stage, capitalised. */
export function stageLabel(stage: string): string {
  return stage.charAt(0).toUpperCase() + stage.slice(1);
}

/** One choice the owner made: a model, or a stage. */
export type PickChange = { model: string } | { stage: string };

/** pick records a choice and leaves the other one as it was. */
export function pick(now: Picked, change: PickChange): Picked {
  return { ...now, ...change };
}

/** What the page says while the plans are read, and when they could not be. */
export const READING = "Reading the plans…";
export const NOT_READ = "The plans could not be read.";

/**
 * statusLine is the sentence the page's live region holds: the plans being read, or not read, whether
 * the catalogue or the figures for the choice; empty once both are in.
 */
export function statusLine(catalogue: Read<PlansView>["status"], figures: FiguresState): string {
  if (catalogue === "loading" || (catalogue === "ready" && figures === "loading")) {
    return READING;
  }
  if (catalogue === "error" || (catalogue === "ready" && figures === "error")) {
    return NOT_READ;
  }
  return "";
}
