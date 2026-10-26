import { InboxSection } from "../inbox";
import { ProjectsSection } from "../projects";

// Side by side the two sections share two grid rows (the heading, then the rest), so a heading that
// wraps moves both cards down together.
const COLUMN =
  "@[51.25rem]:row-span-2 @[51.25rem]:grid @[51.25rem]:grid-cols-1 @[51.25rem]:grid-rows-subgrid";

export function Inbox() {
  return (
    // Two columns once the page's width holds the 368px Projects card, the 32px gap, the header's
    // 26px gutter on both sides and an Actions card at least as wide as Projects: 51.25rem
    // (`MENU_ROOM` in menu.tsx adds the expanded menu to this). The Projects card ends on the
    // header's right edge; beside the menu (lg) the Actions card sits 16px off it. Narrower, the
    // sections stack with Actions first. pages/autonomy.tsx splits at the same width.
    <div className="grid grid-cols-1 gap-y-[calc(var(--spacing-band)*2)] px-padx pt-4 pb-band lg:pl-4 @[51.25rem]:grid-cols-[minmax(0,1fr)_23rem] @[51.25rem]:grid-rows-[auto_auto] @[51.25rem]:gap-x-8 @[51.25rem]:gap-y-0">
      <InboxSection className={COLUMN} />
      <ProjectsSection className={COLUMN} />
    </div>
  );
}
