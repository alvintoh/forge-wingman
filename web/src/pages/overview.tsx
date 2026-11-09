import { useCallback } from "react";
import { InboxSection } from "../inbox";
import { useInbox } from "../inbox-context";
import { useProjects } from "../project-list";
import { ProjectsSection } from "../projects";
import { type PageRefresh, useReturnRead } from "../refresh-read";
import { useRunning } from "../running/read";
import { RunningSection } from "../running/section";

// Two columns once the page's width holds the 368px Projects card, the 32px gap, the header's 26px
// gutter on both sides and an Actions card at least as wide as Projects: 51.25rem (`MENU_ROOM` in
// menu.tsx adds the expanded menu to this). The Projects card ends on the header's right edge; beside
// the menu (lg) the left column sits 16px off it. Narrower, the sections stack with Running now
// first, then Actions, then Projects. pages/autonomy.tsx splits at the same width.
//
// The two columns share three rows, a heading row, a body row and a last row that takes what is
// left, so the Projects card follows the Running now card down when a failed read puts a line under
// its heading.
const COLUMNS =
  "grid grid-cols-1 gap-y-[calc(var(--spacing-band)*2)] px-padx pt-4 pb-band lg:pl-4 @[51.25rem]:grid-cols-[minmax(0,1fr)_23rem] @[51.25rem]:grid-rows-[auto_auto_1fr] @[51.25rem]:items-start @[51.25rem]:gap-x-8 @[51.25rem]:gap-y-0";
// Running now above Actions in the left column: the band between them on the desktop, the sections'
// own 104px apart on a phone.
const LEFT =
  "flex min-w-0 flex-col gap-y-[calc(var(--spacing-band)*2)] @[51.25rem]:col-start-1 @[51.25rem]:row-span-3 @[51.25rem]:row-start-1 @[51.25rem]:grid @[51.25rem]:grid-rows-subgrid @[51.25rem]:gap-y-0";
// A section that is a grid for its subgrid rows gets one minmax(0,1fr) column: an implicit auto
// column grows to a truncated title's full width and pushes the card past its column.
const RUNNING =
  "@[51.25rem]:row-span-2 @[51.25rem]:grid @[51.25rem]:grid-cols-[minmax(0,1fr)] @[51.25rem]:grid-rows-subgrid";
const ACTIONS = "min-w-0 @[51.25rem]:row-start-3 @[51.25rem]:mt-band";
const RIGHT =
  "min-w-0 @[51.25rem]:col-start-2 @[51.25rem]:row-span-3 @[51.25rem]:row-start-1 @[51.25rem]:grid @[51.25rem]:grid-cols-[minmax(0,1fr)] @[51.25rem]:grid-rows-subgrid";

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

  // The oldest read the page draws once the inbox and the projects have landed: the clock's age, and
  // the age a failed re-read names. The runs in flight join it once their read has landed, so a
  // failing read of theirs never hides the clock. Until the first two land the control draws nothing.
  const firstReads =
    inbox.readAt > 0 && projects.readAt > 0 ? Math.min(inbox.readAt, projects.readAt) : 0;
  const readAt =
    firstReads > 0 && inFlight.readAt > 0 ? Math.min(firstReads, inFlight.readAt) : firstReads;

  const fetching = inboxFetching || projectsFetching || runningFetching;
  const ask = useReturnRead({ readAt, fetching, refresh });
  const pageRefresh: PageRefresh = {
    readAt,
    stale: inbox.stale || projects.stale || inFlight.stale,
    fetching,
    // The age is the wall clock read at this draw; the page is neither memoised nor hydrated, so
    // there is nowhere else the instant could come from.
    // oxlint-disable-next-line react/purity
    now: Date.now(),
    ask,
  };

  return (
    <div className={COLUMNS}>
      <div className={LEFT}>
        <RunningSection
          read={running}
          projects={projects}
          refresh={pageRefresh}
          className={RUNNING}
        />
        <div className={ACTIONS}>
          <InboxSection />
        </div>
      </div>
      <ProjectsSection className={RIGHT} read={read} />
    </div>
  );
}
