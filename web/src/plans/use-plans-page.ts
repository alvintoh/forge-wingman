import { useReducer } from "react";
import type { Read } from "../stats/use-read";
import {
  figuresState,
  figuresUrl,
  isPlansView,
  pick,
  PLANS_URL,
  reloadFor,
  resolveChoice,
} from "./view";
import type { Choice, FiguresState, Picked, PickChange, PlansView } from "./view";

export type PlansPageProps = {
  catalogue: Read<PlansView>;
  // What the figures read holds for the choice's url.
  figures: FiguresState;
  choice: Choice | null;
  onPick: (change: PickChange) => void;
  onRetry: () => void;
};

/** The read a page makes: useRead, or a stand-in a test hands it. */
export type PlansReader = (
  url: string | null,
  guard: (body: unknown) => body is PlansView,
  token: string,
) => Read<PlansView> & { reload: () => void };

/**
 * usePlansPage reads the plans once for the models and the stages' requests, then again for the
 * figures at the model and stage chosen: the first model some plan prices above $0 and the first
 * stage with a recorded request until the owner chooses. A new choice reads the figures again and
 * leaves the card where it is; nothing is read for the figures while there is nothing to price.
 * `initial` is the choice to start from, none unless a test says.
 */
export function usePlansPage(read: PlansReader, initial: Picked = {}): PlansPageProps {
  const catalogue = read(PLANS_URL, isPlansView, "plansNotRead");
  const [picked, onPick] = useReducer(pick, initial);
  const choice = catalogue.status === "ready" ? resolveChoice(catalogue.body, picked) : null;
  const url = figuresUrl(choice);
  const figures = read(url, isPlansView, "plansFiguresNotRead");
  return {
    catalogue,
    figures: figuresState(figures, url),
    choice,
    onPick,
    onRetry: reloadFor(catalogue, figures),
  };
}
