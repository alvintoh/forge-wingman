import { captionClass } from "../caption";
import { RadioChoice } from "../filter-radio";
import type { Choice, ListedShape, ModelGroup } from "./view";
import { stageLabel, tokensLine } from "./view";

// The two sentences under the select. The wording is the drawing's; the second ends on the one case
// a figure cannot be converted, so a limit shown as the vendor published it is explained.
const LEGEND =
  "Exact is the plan's own rate card and the shape's token counts. Estimated means an input is a published approximation. A limit that cannot be converted stays as the vendor published it.";

const NO_SHAPES = "No stage has enough recorded runs to show a request.";

// The ids tie each label to its control; the page holds one card, so they are fixed.
const TITLE_ID = "price-card-title";
const MODEL_ID = "price-model";
const SHAPE_LABEL_ID = "price-shape-label";

// Every @[49rem] below is the card's inner width from which the model and the legend sit in a 22.5rem
// column beside the shape rows, which then run on one line; narrower, everything stacks and a row's
// counts sit under its name. 49rem holds the 360px column, a 40px gap and a row of 380px.

type ShapeRowProps = {
  listed: ListedShape;
  checked: boolean;
  // Whether the row is the group's one tab stop.
  tabStop: boolean;
  onChoose: (stage: string) => void;
};

function ShapeRow({ listed, checked, tabStop, onChoose }: ShapeRowProps) {
  const nameId = `price-shape-${listed.stage}`;
  const countsId = `${nameId}-counts`;
  function choose() {
    onChoose(listed.stage);
  }
  return (
    <div
      className={`relative flex min-h-15 flex-col pt-3 pl-8 @[49rem]:min-h-11 @[49rem]:flex-row @[49rem]:items-center @[49rem]:justify-between @[49rem]:gap-4 @[49rem]:pt-0`}
    >
      <RadioChoice
        labelledBy={`${nameId} ${countsId}`}
        checked={checked}
        onChoose={choose}
        arrows={{ tabStop }}
        bulletClass={`top-3 @[49rem]:top-1/2 @[49rem]:-translate-y-1/2`}
      />
      <span id={nameId} className="text-body leading-5.5 font-semibold text-ink">
        {stageLabel(listed.stage)}
      </span>
      <span id={countsId} className="min-w-0 font-mono text-cap leading-4 text-ink2">
        {tokensLine(listed.tokens)}
      </span>
    </div>
  );
}

type PriceCardProps = {
  // The select's models by plan, in the server's order.
  groups: readonly ModelGroup[];
  shapes: readonly ListedShape[];
  // The model and stage the figures are priced at; null while no stage has a recorded request.
  choice: Choice | null;
  // Whether the figures for the choice are being read.
  busy: boolean;
  onModel: (model: string) => void;
  onStage: (stage: string) => void;
};

/**
 * PriceCard is the card that states what a request is: the model it is priced at and the shape of
 * its tokens, one radio row per stage that has a recorded request. The select is 48px high and its
 * value 17px, so a phone does not zoom in on it; the figures themselves are drawn by the cards
 * under this one.
 */
export function PriceCard({ groups, shapes, choice, busy, onModel, onStage }: PriceCardProps) {
  // With no radio checked the first is the tab stop, so the group can still be entered.
  const chosen = shapes.some((listed) => listed.stage === choice?.stage);

  function changeModel(event: React.ChangeEvent<HTMLSelectElement>) {
    onModel(event.target.value);
  }
  return (
    <section
      aria-labelledby={TITLE_ID}
      aria-busy={busy}
      className="card-frame @container pt-6 pb-4 max-sm:-mx-2.5"
    >
      <h2 id={TITLE_ID} className={`${captionClass} leading-5 text-ink2`}>
        price a request
      </h2>
      <div
        className={`mt-3.75 grid grid-cols-1 @[49rem]:grid-cols-[22.5rem_minmax(0,1fr)] @[49rem]:grid-rows-[auto_1fr] @[49rem]:gap-x-10`}
      >
        <div className="min-w-0">
          <label htmlFor={MODEL_ID} className={`${captionClass} mb-1.5 block leading-4 text-ink2`}>
            model
          </label>
          <div className="relative">
            <select
              id={MODEL_ID}
              value={choice?.model ?? groups[0]?.models[0]?.id ?? ""}
              onChange={changeModel}
              className="block h-12 w-full cursor-pointer appearance-none truncate rounded-tile border border-ink2 bg-ground pr-12 pl-3.5 font-mono text-head text-ink"
            >
              {groups.map((group) => (
                <optgroup key={group.plan} label={group.plan}>
                  {group.models.map((model) => (
                    <option key={model.id} value={model.id}>
                      {model.name}
                    </option>
                  ))}
                </optgroup>
              ))}
            </select>
            <svg
              aria-hidden="true"
              width="10"
              height="5"
              viewBox="0 0 10 5"
              fill="none"
              stroke="currentColor"
              strokeWidth="1.75"
              strokeLinecap="round"
              strokeLinejoin="round"
              className="pointer-events-none absolute top-[21px] right-4 overflow-visible text-ink2"
            >
              <path d="M0 0l5 5l5 -5" />
            </svg>
          </div>
        </div>
        <div
          className={`mt-5 min-w-0 @[49rem]:col-start-2 @[49rem]:row-span-2 @[49rem]:row-start-1 @[49rem]:mt-0`}
        >
          <p id={SHAPE_LABEL_ID} className={`${captionClass} mb-1.5 leading-4 text-ink2`}>
            request shape
          </p>
          {shapes.length === 0 ? (
            <p className="text-label leading-6.5 text-ink2">{NO_SHAPES}</p>
          ) : (
            <div role="radiogroup" aria-labelledby={SHAPE_LABEL_ID}>
              {shapes.map((listed, at) => (
                <ShapeRow
                  key={listed.stage}
                  listed={listed}
                  checked={listed.stage === choice?.stage}
                  tabStop={chosen ? listed.stage === choice?.stage : at === 0}
                  onChoose={onStage}
                />
              ))}
            </div>
          )}
        </div>
        <p className={`mt-2 text-cap leading-4 text-ink2 @[49rem]:mt-4`}>{LEGEND}</p>
      </div>
    </section>
  );
}
