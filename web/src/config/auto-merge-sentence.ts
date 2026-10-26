import { barOf, whole } from "../autonomy/format";
import type { MergeState } from "../autonomy/types";

// What the shown switch does, with the window and the bar the endpoint serves.
export function autoMergeSentence(on: boolean, merge: MergeState): string {
  return on
    ? `Size S and M PRs merge themselves while the trailing ${merge.window} is ${whole(barOf(merge.limit))} or less.`
    : "Every PR waits for you to merge it.";
}
