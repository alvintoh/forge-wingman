import { count, modelName, stageLabel, tokensLine, UNCONFIRMED } from "./view";
import type { Ceiling, Choice, Field, Mark, Plan, PlansView } from "./view";

/**
 * One thing a cell shows, as data, so the table's columns and a phone's cards draw the same cells
 * and a test reads what a cell says without a DOM. A value nothing confirms is the `glyph` with
 * `unconfirmed`, never an empty cell.
 */
export type Piece =
  | { kind: "text"; text: string }
  // A value read as a word beside the figures, such as "No figure".
  | { kind: "label"; text: string }
  // A word set as the figure is, where the plan's rate card prices every token at $0.
  | { kind: "headline"; text: string }
  | { kind: "limit"; text: string }
  | { kind: "requests"; text: string }
  | { kind: "note"; text: string }
  | { kind: "list"; items: string[] }
  | { kind: "glyph"; glyph: "yes" | "no" | "unconfirmed"; word: string }
  | { kind: "figure"; value: string }
  | { kind: "peak"; value: string; words: string }
  | { kind: "mark"; mark: Mark }
  | { kind: "shape"; text: string };

export type Row = { label: string; pieces: Piece[] };

export type Section = { title: string; rows: Row[] };

/** One plan's column: its name and id, then every section of the comparison in the order drawn. */
export type Column = { id: string; name: string; sections: Section[] };

const MICROS_PER_DOLLAR = 1_000_000;

/**
 * dollars writes an amount in micros as the drawing writes money: whole dollars as they are, else
 * to the cent or as many places as it holds (up to six). It is exact, since it never divides a
 * float.
 */
export function dollars(micros: number): string {
  const rest = micros % MICROS_PER_DOLLAR;
  const head = `$${count((micros - rest) / MICROS_PER_DOLLAR)}`;
  if (rest === 0) {
    return head;
  }
  return `${head}.${String(rest).padStart(6, "0").replace(/0+$/, "").padEnd(2, "0")}`;
}

/** monthlyPrice is the plan's price as the drawing words it: "$10 a month", or "$0, free tier". */
export function monthlyPrice(micros: number): string {
  return micros === 0 ? "$0, free tier" : `${dollars(micros)} a month`;
}

/** planName is the heading over a column: the plan's id as words, since the endpoint carries no other name. */
export function planName(id: string): string {
  return id.split("-").map(stageLabel).join(" ");
}

function isKnown<T>(value: Field<T>): value is T {
  return value !== UNCONFIRMED;
}

const unconfirmed: Piece = { kind: "glyph", glyph: "unconfirmed", word: UNCONFIRMED };

function text(value: string): Piece {
  return { kind: "text", text: value };
}

function note(value: string): Piece {
  return { kind: "note", text: value };
}

function bareModel(model: string): string {
  return model.slice(model.indexOf("/") + 1);
}

function peakWords(multiplier: number | undefined): string {
  return multiplier === undefined ? "at peak" : `at peak, price ×${multiplier}`;
}

function peakMultiplier(plan: Plan, model: string): number | undefined {
  return plan.models.find((priced) => priced.model === model)?.peak?.multiplier;
}

function requestsPerDollar(plan: Plan, choice: Choice | null): Piece[] {
  const figure = plan.requests_per_dollar;
  if (isKnown(figure)) {
    const pieces: Piece[] = [{ kind: "figure", value: count(figure.off_peak) }];
    if (figure.has_peak) {
      pieces.push({
        kind: "peak",
        value: count(figure.peak),
        words: peakWords(peakMultiplier(plan, figure.shape.model)),
      });
    }
    pieces.push(
      { kind: "mark", mark: figure.mark },
      { kind: "shape", text: tokensLine(figure.shape) },
    );
    return pieces;
  }
  if (choice === null) {
    return [{ kind: "label", text: "No figure" }];
  }
  const card = plan.models.find((priced) => priced.model === choice.model);
  if (card === undefined) {
    return [
      { kind: "label", text: "No figure" },
      note(`No rate card for ${bareModel(choice.model)} on this plan.`),
    ];
  }
  if (
    card.input_usd_per_mtok === 0 &&
    card.output_usd_per_mtok === 0 &&
    card.cache_read_usd_per_mtok === 0
  ) {
    return [{ kind: "headline", text: "Free" }, note("Its rate card prices every token at $0.")];
  }
  return [{ kind: "label", text: "No figure" }];
}

// A ceiling's dollar limit as published, then what it buys at the figure's rates; a limit with no
// conversion shows alone. The counts carry no mark of their own.
function ceiling(field: Field<Ceiling>): Piece[] {
  if (!isKnown(field)) {
    return [unconfirmed];
  }
  const pieces: Piece[] = [{ kind: "limit", text: dollars(field.limit_micros) }];
  const { requests } = field;
  if (requests !== null) {
    pieces.push({ kind: "requests", text: `${count(requests.off_peak)} requests off-peak` });
    if (requests.has_peak) {
      pieces.push({ kind: "requests", text: `${count(requests.peak)} requests at peak` });
    }
  }
  return pieces;
}

function fact(value: Field<boolean>, prefix: string): Piece {
  if (!isKnown(value)) {
    return { kind: "glyph", glyph: "unconfirmed", word: `${prefix}${UNCONFIRMED}` };
  }
  return { kind: "glyph", glyph: value ? "yes" : "no", word: `${prefix}${value ? "yes" : "no"}` };
}

function verdict(value: Plan["verdict"]): Piece {
  if (!isKnown(value)) {
    return unconfirmed;
  }
  return { kind: "glyph", glyph: value === "allowed" ? "yes" : "no", word: value };
}

// A harness is named before its word only when a plan has several.
function harnessFacts(plan: Plan, pick: (h: Plan["harnesses"][number]) => Field<boolean>): Piece[] {
  if (plan.harnesses.length === 0) {
    return [unconfirmed];
  }
  const named = plan.harnesses.length > 1;
  return plan.harnesses.map((h) => fact(pick(h), named ? `${h.harness}: ` : ""));
}

function features(plan: Plan): Piece[] {
  return plan.features.length === 0 ? [unconfirmed] : [{ kind: "list", items: plan.features }];
}

function sectionsOf(plan: Plan, choice: Choice | null): Section[] {
  const price = plan.monthly_price_micros;
  const strongest = plan.strongest;
  const cap = plan.strongest_cap_micros;
  return [
    {
      title: "Cost",
      rows: [
        {
          label: "Monthly price",
          pieces: [isKnown(price) ? text(monthlyPrice(price)) : unconfirmed],
        },
        { label: "Requests per $1", pieces: requestsPerDollar(plan, choice) },
      ],
    },
    {
      title: "Ceilings",
      rows: [
        { label: "5-hour ceiling", pieces: ceiling(plan.five_hour) },
        { label: "Weekly ceiling", pieces: ceiling(plan.weekly) },
        { label: "Monthly ceiling", pieces: ceiling(plan.monthly) },
      ],
    },
    {
      title: "Models",
      rows: [
        {
          label: "Strongest model",
          pieces: [isKnown(strongest) ? text(modelName(plan.plan, strongest.model)) : unconfirmed],
        },
        {
          label: "Its per-model cap",
          pieces: [isKnown(cap) ? text(`${dollars(cap)} a month`) : unconfirmed],
        },
      ],
    },
    { title: "Features", rows: [{ label: "Features", pieces: features(plan) }] },
    {
      title: "Harnesses",
      rows: [
        {
          label: "Harness",
          pieces:
            plan.harnesses.length === 0
              ? [unconfirmed]
              : plan.harnesses.map((h) => text(h.harness)),
        },
        { label: "Runs headless", pieces: harnessFacts(plan, (h) => h.headless) },
        { label: "Structured output", pieces: harnessFacts(plan, (h) => h.structured_output) },
        {
          label: "Planning run denies edits and shell",
          pieces: harnessFacts(plan, (h) => h.denies_planning_edits),
        },
      ],
    },
    { title: "Terms", rows: [{ label: "Unattended use", pieces: [verdict(plan.verdict)] }] },
  ];
}

/**
 * compareColumns is one column per plan, in the server's order, each holding the same sections
 * and rows, so a table reads across them and a phone's card reads down one. `choice` is the model
 * and stage the figures were priced at, null while none was.
 */
export function compareColumns(view: PlansView, choice: Choice | null): Column[] {
  return view.plans.map((plan) => ({
    id: plan.plan,
    name: planName(plan.plan),
    sections: sectionsOf(plan, choice),
  }));
}
