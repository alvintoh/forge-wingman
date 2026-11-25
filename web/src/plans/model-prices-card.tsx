import { Fragment } from "react";
import { captionClass } from "../caption";
import type { Column } from "./compare";

// The id ties the card to its heading; the page holds one, so it is fixed.
const TITLE_ID = "model-prices-title";

// The wide table's columns, from the card's width: the model and the three rates have a width each
// and the rate's words take what is left. Every @[47rem] below is the width of the column the
// cards sit in from which the compare card draws a table too, and under it each plan's own card
// ends on its models' prices, so this card stands down there.
const headClass = "pb-2 text-cap leading-4 font-semibold tracking-[0.3px] text-ink2";
const numberClass = "text-right font-mono text-label leading-4.5";

type ModelPricesCardProps = {
  columns: readonly Column[];
};

/**
 * ModelPricesCard lists what each plan's models cost in US dollars per million tokens: the
 * uncached input, the output and the cache read, in one group per plan under its name and id. A
 * model priced by the hour or by context length has a row for each price. A plan with no models
 * has no group, and the card is left out when no plan has any.
 */
export function ModelPricesCard({ columns }: ModelPricesCardProps) {
  const priced = columns.filter((column) => column.prices.length > 0);
  if (priced.length === 0) {
    return null;
  }
  return (
    <div className="@container min-w-0">
      <section aria-labelledby={TITLE_ID} className="card-frame hidden pt-6 pb-4 @[47rem]:block">
        <div className="flex items-baseline justify-between gap-4">
          <h2 id={TITLE_ID} className={`${captionClass} leading-5 text-ink2`}>
            model prices
          </h2>
          <p className="text-cap leading-4 text-ink2">USD per 1M tokens</p>
        </div>
        <table className="mt-3 w-full table-fixed border-collapse text-left">
          <colgroup>
            <col className="w-54 @[64rem]:w-75" />
            <col />
            <col className="w-26 @[64rem]:w-30" />
            <col className="w-26 @[64rem]:w-30" />
            <col className="w-26 @[64rem]:w-30" />
          </colgroup>
          <thead>
            <tr className="border-b border-hair">
              <th scope="col" className={headClass}>
                Model
              </th>
              <th scope="col" className={headClass}>
                Rate
              </th>
              <th scope="col" className={`${headClass} text-right`}>
                Uncached input
              </th>
              <th scope="col" className={`${headClass} text-right`}>
                Output
              </th>
              <th scope="col" className={`${headClass} text-right`}>
                Cache read
              </th>
            </tr>
          </thead>
          {priced.map((column, plan) => {
            const lastPlan = plan === priced.length - 1;
            return (
              <tbody key={`${plan}-${column.id}`}>
                <tr>
                  <th
                    scope="rowgroup"
                    colSpan={2}
                    className="h-10 pt-3.5 text-left align-top text-label leading-5 font-semibold break-words text-ink"
                  >
                    {column.name}
                  </th>
                  <td
                    colSpan={3}
                    className="pt-3.5 text-right align-top font-mono text-cap leading-4 break-words text-ink2"
                  >
                    {column.id}
                  </td>
                </tr>
                {column.prices.map((model, at) => {
                  const lastModel = at === column.prices.length - 1;
                  return (
                    <Fragment key={`${at}-${model.name}`}>
                      {model.tiers.map((tier, tierAt) => {
                        const last = lastPlan && lastModel && tierAt === model.tiers.length - 1;
                        return (
                          <tr
                            key={`${tierAt}-${tier.label}`}
                            className={last ? "" : "border-b border-hair"}
                          >
                            {tierAt === 0 ? (
                              <th
                                scope="row"
                                className="py-3 pr-4 text-left align-top font-mono text-label leading-4.5 font-semibold break-words text-ink"
                              >
                                {model.name}
                              </th>
                            ) : (
                              <td className="py-3 pr-4 align-top">
                                <span className="sr-only">{model.name}</span>
                              </td>
                            )}
                            <td className="py-3 pr-4 align-top">
                              <p className="text-label leading-4.5 text-ink2">{tier.label}</p>
                              {tier.window !== null && (
                                <p className="text-cap leading-4 text-ink2">{tier.window}</p>
                              )}
                            </td>
                            <td className={`${numberClass} py-3 align-top text-ink`}>
                              {tier.input}
                            </td>
                            <td className={`${numberClass} py-3 align-top text-ink`}>
                              {tier.output}
                            </td>
                            <td className={`${numberClass} py-3 align-top text-ink`}>
                              {tier.cacheRead}
                            </td>
                          </tr>
                        );
                      })}
                    </Fragment>
                  );
                })}
              </tbody>
            );
          })}
        </table>
      </section>
    </div>
  );
}
