import { useCallback } from "react";
import { InboxSection } from "../inbox";
import { useInbox } from "../inbox-context";
import { useProjects } from "../project-list";
import { ProjectsSection } from "../projects";
import { RefreshStrip } from "../refresh-strip";
import { useRunning } from "../running/read";
import { RunningSection } from "../running/section";

// Two columns once the page's width holds the 368px Projects card, the 32px gap, the header's 26px
// gutter on both sides and an Actions card at least as wide as Projects: 51.25rem (`MENU_ROOM` in
// menu.tsx adds the expanded menu to this). The Projects card ends on the header's right edge; beside
// the menu (lg) the left column sits 16px off it. Narrower, the sections stack with Running now
// first, then Actions, then Projects. pages/autonomy.tsx splits at the same width.
const COLUMNS =
  "grid grid-cols-1 gap-y-[calc(var(--spacing-band)*2)] px-padx pt-4 pb-band lg:pl-4 @[51.25rem]:grid-cols-[minmax(0,1fr)_23rem] @[51.25rem]:items-start @[51.25rem]:gap-x-8";
// Running now above Actions in the left column: the band between them on the desktop, the sections'
// own 104px apart on a phone.
const LEFT =
  "flex min-w-0 flex-col gap-y-[calc(var(--spacing-band)*2)] @[51.25rem]:col-start-1 @[51.25rem]:row-start-1 @[51.25rem]:gap-y-band";
const RIGHT = "min-w-0 @[51.25rem]:col-start-2 @[51.25rem]:row-start-1";

export function Inbox() {
  const { state: inbox, refresh: refreshInbox, fetching: inboxFetching } = useInbox();
  const read = useProjects();
  const { state: projects, refresh: refreshProjects, fetching: projectsFetching } = read;
  const running = useRunning();
  const { state: inFlight, refresh: refreshRunning, fetching: runningFetching } = running;
  const refresh = useCallback(() => {
    refreshInbox();
    refreshProjects();
    refreshRunning();
  }, [refreshInbox, refreshProjects, refreshRunning]);

  // The oldest read the page draws once the inbox and the projects have landed: the strip's age, and
  // the age a failed re-read names. The runs in flight join it once their read has landed, so a
  // failing read of theirs never hides the strip. Until the first two land the strip draws nothing.
  const firstReads =
    inbox.readAt > 0 && projects.readAt > 0 ? Math.min(inbox.readAt, projects.readAt) : 0;
  const readAt =
    firstReads > 0 && inFlight.readAt > 0 ? Math.min(firstReads, inFlight.readAt) : firstReads;

  return (
    <div>
      <div className="px-padx lg:pl-4">
        <RefreshStrip
          readAt={readAt}
          stale={inbox.stale || projects.stale || inFlight.stale}
          fetching={inboxFetching || projectsFetching || runningFetching}
          refresh={refresh}
        />
      </div>
      <div className={COLUMNS}>
        <div className={LEFT}>
          <RunningSection read={running} projects={projects} />
          <InboxSection />
        </div>
        <ProjectsSection className={RIGHT} read={read} />
      </div>
    </div>
  );
}
