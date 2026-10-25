import { pillSetWidth } from "../button-style";
import { SmallPill } from "../pill-button";
import { KIND_CHIPS, type KindChip } from "./kind";

// One width for the set, with the tick's room in it, so a chip does not resize when it is pressed.
const CHIP_WIDTH = pillSetWidth(KIND_CHIPS, 14);

type KindChipsProps = {
  chip: KindChip | null;
  onChange: (chip: KindChip | null) => void;
};

/** KindChips are the toggles above the list; pressing the pressed one clears it. */
export function KindChips({ chip, onChange }: KindChipsProps) {
  return (
    <div role="group" aria-label="Filter by kind" className="flex flex-wrap items-center gap-x-1.5">
      {KIND_CHIPS.map((kind) => {
        const on = kind === chip;
        return (
          <SmallPill
            key={kind}
            kind={on ? "primary" : "secondary"}
            width={CHIP_WIDTH}
            label={kind}
            ticked={on}
            aria-pressed={on}
            onClick={() => onChange(on ? null : kind)}
          />
        );
      })}
    </div>
  );
}
