import { pillSetWidth } from "../button-style";
import { SmallPill } from "../pill-button";
import type { Read } from "../stats/use-read";
import { useMedia } from "../use-media";
import {
  RUNS_AT_ONCE_PRESETS,
  parseRunsAtOnce,
  presetsUnder,
  rangeError,
  rangeSentence,
  reasonWords,
} from "./runs-at-once";
import type { RunsAtOnceView } from "./runs-at-once";

const TITLE_ID = "config-runs-at-once";
const SENTENCE_ID = "config-runs-at-once-says";
const ERROR_ID = "config-runs-at-once-error";

// One width for the presets, with the tick's room in it, so a preset does not resize when chosen.
const PRESET_WIDTH = pillSetWidth(RUNS_AT_ONCE_PRESETS.map(String), 14);
// One width for USE R whatever R is, from the widest label the cap allows.
const USE_WIDTH = pillSetWidth(["USE 20"]);

// From this width the presets sit left of the field, centred on the name's capitals; under it the
// name, the presets and the field do not fit one line, so the presets take their own row.
const PRESETS_BESIDE = "(min-width: 520px)";

type PresetsProps = {
  platformCap: number;
  // The number the field holds, null while it holds none the cap allows.
  field: number | null;
  disabled: boolean;
  onFill: (text: string) => void;
};

/**
 * Presets fill the field with their number and save nothing. The one equal to the field's number
 * is chosen (pressed, with a tick), never dead; one above the platform cap is not offered.
 */
export function Presets({ platformCap, field, disabled, onFill }: PresetsProps) {
  return (
    <div
      role="group"
      aria-label="Runs at once presets"
      className="flex shrink-0 items-center gap-2"
    >
      {presetsUnder(platformCap).map((preset) => {
        const on = preset === field;
        return (
          <SmallPill
            key={preset}
            kind={on ? "primary" : "secondary"}
            width={PRESET_WIDTH}
            label={String(preset)}
            ticked={on}
            aria-pressed={on}
            aria-label={`Fill in ${preset}`}
            disabled={disabled}
            onClick={() => onFill(String(preset))}
          />
        );
      })}
    </div>
  );
}

type RecommendedProps = {
  view: RunsAtOnceView;
  field: number | null;
  disabled: boolean;
  onFill: (text: string) => void;
};

/**
 * Recommended is the rule's N and why, worded from the saved N. USE R fills the field with R and
 * saves nothing; it shows only while R differs from the number in the field.
 */
export function Recommended({ view, field, disabled, onFill }: RecommendedProps) {
  const r = view.recommendation;
  if (r === null) {
    return <p className="mt-3 text-ink2">The recommendation could not be read.</p>;
  }
  return (
    <>
      <div className="mt-1.5 flex min-h-11 items-center justify-between gap-4">
        <p className="font-semibold text-ink">Recommended: {r.n}</p>
        {r.n !== field && (
          <SmallPill
            kind="secondary"
            width={USE_WIDTH}
            label={`USE ${r.n}`}
            disabled={disabled}
            onClick={() => onFill(String(r.n))}
          />
        )}
      </div>
      <p className="text-ink2">{reasonWords(r, view.runs_at_once, view.platform_cap)}</p>
    </>
  );
}

type RunsAtOnceSectionProps = {
  read: Read<RunsAtOnceView>;
  // The saved N's view: the read's answer, or the answer to the last save.
  view: RunsAtOnceView | null;
  // The field's text, null until N is read.
  text: string | null;
  invalid: boolean;
  disabled: boolean;
  onFill: (text: string) => void;
};

/**
 * RunsAtOnceSection is the owner's N: the name with its number field, the presets, what N is and
 * its range, then the N the concurrency rule recommends and why. Every control fills the field;
 * the page's one SAVE saves it.
 */
export function RunsAtOnceSection({
  read,
  view,
  text,
  invalid,
  disabled,
  onFill,
}: RunsAtOnceSectionProps) {
  const beside = useMedia(PRESETS_BESIDE);
  const field = view === null || text === null ? null : parseRunsAtOnce(text, view.platform_cap);
  const presets = view !== null && (
    <Presets platformCap={view.platform_cap} field={field} disabled={disabled} onFill={onFill} />
  );
  return (
    <section
      aria-labelledby={TITLE_ID}
      aria-busy={read.status === "loading"}
      className="border-t border-hair pt-3.5 pb-5 text-label leading-5"
    >
      <div className="flex min-h-11 items-center justify-between gap-3">
        <h2 id={TITLE_ID} className="min-w-0 text-body font-semibold text-ink">
          Runs at once (N)
        </h2>
        {view !== null && text !== null && (
          <div className="flex shrink-0 items-center gap-3">
            {beside && presets}
            <input
              type="text"
              inputMode="numeric"
              autoComplete="off"
              value={text}
              aria-labelledby={TITLE_ID}
              aria-describedby={invalid ? `${ERROR_ID} ${SENTENCE_ID}` : SENTENCE_ID}
              aria-invalid={invalid}
              disabled={disabled}
              onChange={(event) => onFill(event.target.value)}
              className={`h-11 w-18 rounded-tile bg-card pl-3.5 font-mono text-head text-ink focus-visible:outline-3 focus-visible:outline-offset-2 focus-visible:outline-accent disabled:bg-track disabled:text-muted ${invalid ? "border-2 border-stop" : "border border-ink2 disabled:border-track"}`}
            />
          </div>
        )}
      </div>
      {!beside && view !== null && text !== null && <div className="mt-2">{presets}</div>}
      {read.status === "loading" && (
        <p role="status" className="text-ink2">
          Reading runs at once…
        </p>
      )}
      {read.status === "error" && (
        <p role="alert" className="text-ink2">
          Runs at once could not be read.
        </p>
      )}
      {view !== null && (
        <>
          {invalid && (
            <p id={ERROR_ID} className="mt-1 font-semibold text-stop">
              {rangeError(view.platform_cap)}
            </p>
          )}
          <p id={SENTENCE_ID} className="mt-1 text-ink">
            {rangeSentence(view.platform_cap)}
          </p>
          <Recommended view={view} field={field} disabled={disabled} onFill={onFill} />
        </>
      )}
    </section>
  );
}
