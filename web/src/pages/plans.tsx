import { captionClass } from "../caption";
import { PriceCard } from "../plans/price-card";
import { PlansError, PlansLoading } from "../plans/plans-status";
import { usePlansPage } from "../plans/use-plans-page";
import type { PlansPageProps } from "../plans/use-plans-page";
import { listedShapes, modelGroups, statusLine } from "../plans/view";
import { useRead } from "../stats/use-read";

/**
 * PlansPageView draws the page for the reads it is given: the price-a-request card, once the plans
 * are known. The cards the figures fill (the plans side by side, the model prices) go under it, in
 * the one column; the figures' own skeleton and failure stand in that place until they do. One live
 * region holds the sentence for whichever of them shows, and stays in the page so a change to its
 * text is announced.
 */
export function PlansPageView({ catalogue, figures, choice, onPick, onRetry }: PlansPageProps) {
  function chooseModel(model: string) {
    onPick({ model });
  }

  function chooseStage(stage: string) {
    onPick({ stage });
  }

  return (
    <section className="mx-auto max-w-desk px-padx pt-4 pb-band">
      <h1 className={`${captionClass} py-[3.25px] text-ink2`}>plans</h1>
      <p role="status" className="sr-only">
        {statusLine(catalogue.status, figures)}
      </p>
      <div className="mt-4 flex min-w-0 flex-col gap-y-2 @[51.25rem]:gap-y-4">
        {catalogue.status === "loading" && <PlansLoading />}
        {catalogue.status === "error" && <PlansError onRetry={onRetry} />}
        {catalogue.status === "ready" && (
          <>
            <PriceCard
              groups={modelGroups(catalogue.body)}
              shapes={listedShapes(catalogue.body)}
              choice={choice}
              busy={figures === "loading"}
              onModel={chooseModel}
              onStage={chooseStage}
            />
            {figures === "loading" && <PlansLoading />}
            {figures === "error" && <PlansError onRetry={onRetry} />}
          </>
        )}
      </div>
    </section>
  );
}

export function Plans() {
  return <PlansPageView {...usePlansPage(useRead)} />;
}
