import { useCallback } from "react";
import { InboxSection } from "../inbox";
import { useInbox } from "../inbox-context";
import { useProjects } from "../project-list";
import { ProjectsSection } from "../projects";
import { RefreshStrip } from "../refresh-strip";

// Side by side the two sections share two grid rows (the heading, then the rest), so a heading that
// wraps moves both cards down together.
const COLUMN =
  "@[51.25rem]:row-span-2 @[51.25rem]:grid @[51.25rem]:grid-cols-1 @[51.25rem]:grid-rows-subgrid";

export function Inbox() {
  const { state: inbox, refresh: refreshInbox, fetching: inboxFetching } = useInbox();
  const read = useProjects();
  const { state: projects, refresh: refreshProjects, fetching: projectsFetching } = read;
  const refresh = useCallback(() => {
    refreshInbox();
    refreshProjects();
  }, [refreshInbox, refreshProjects]);

  // The oldest read the page draws: the strip's age, and the age a failed re-read names.
  const reads = [inbox.readAt, projects.readAt].filter((at) => at > 0);
  const readAt = reads.length === 0 ? 0 : Math.min(...reads);

  return (
    <div>
      <div className="px-padx pt-4 lg:pl-4">
        <RefreshStrip
          readAt={readAt}
          stale={inbox.stale || projects.stale}
          fetching={inboxFetching || projectsFetching}
          refresh={refresh}
        />
      </div>
      {/* Two columns once the page's width holds the 368px Projects card, the 32px gap, the header's
          26px gutter on both sides and an Actions card at least as wide as Projects: 51.25rem
          (`MENU_ROOM` in menu.tsx adds the expanded menu to this). The Projects card ends on the
          header's right edge; beside the menu (lg) the Actions card sits 16px off it. Narrower, the
          sections stack with Actions first. pages/autonomy.tsx splits at the same width. */}
      <div className="grid grid-cols-1 gap-y-[calc(var(--spacing-band)*2)] px-padx pt-4 pb-band lg:pl-4 @[51.25rem]:grid-cols-[minmax(0,1fr)_23rem] @[51.25rem]:grid-rows-[auto_auto] @[51.25rem]:gap-x-8 @[51.25rem]:gap-y-0">
        <InboxSection className={COLUMN} />
        <ProjectsSection className={COLUMN} read={read} />
      </div>
    </div>
  );
}
