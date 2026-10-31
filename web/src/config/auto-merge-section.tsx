import { rateLine } from "../autonomy/format";
import type { AutonomyView } from "../autonomy/types";
import type { Read } from "../stats/use-read";
import { SwitchRow } from "./switch";

type AutoMergeSectionProps = {
  read: Read<AutonomyView>;
  // The switch shown: the staged value, else the saved one; null until the state is read.
  on: boolean | null;
  disabled: boolean;
  onToggle: () => void;
};

/**
 * AutoMergeSection is the auto-merge switch, a sentence on the value it shows, and the rate line
 * from the saved state, which a staged change does not move.
 */
export function AutoMergeSection({ read, on, disabled, onToggle }: AutoMergeSectionProps) {
  const merge = read.status === "ready" ? read.body.auto_merge : null;
  return (
    <section aria-busy={read.status === "loading"} className="pt-3.5 pb-5">
      <SwitchRow name="Auto-merge" level="h2" on={on} disabled={disabled} onToggle={onToggle}>
        {/* While the state loads the sentence's place holds two lines. */}
        {read.status === "loading" && (
          <p role="status" className="min-h-10">
            Reading the auto-merge state…
          </p>
        )}
        {read.status === "error" && (
          <p role="alert" className="min-h-10">
            The auto-merge state could not be read.
          </p>
        )}
        {merge !== null && on !== null && (
          <p>{on ? "Size S and M PRs merge themselves." : "Every PR waits for you to merge it."}</p>
        )}
      </SwitchRow>
      {merge !== null && <p className="text-label leading-5 text-ink2">{rateLine(merge)}</p>}
    </section>
  );
}
