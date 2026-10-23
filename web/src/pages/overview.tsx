import { useLayoutEffect, useRef, useState } from "react";
import { InboxSection } from "../inbox";
import { ProjectsSection } from "../projects";

// useTwoColumns asks the grid whether it lays out two columns, so the container query that decides
// it is the only place the width is written.
function useTwoColumns(grid: React.RefObject<HTMLDivElement | null>) {
  const [twoColumns, setTwoColumns] = useState(false);
  useLayoutEffect(() => {
    const element = grid.current;
    if (element === null) {
      return;
    }
    const read = () =>
      setTwoColumns(getComputedStyle(element).gridTemplateColumns.trim().split(/\s+/).length > 1);
    read();
    const observer = new ResizeObserver(read);
    observer.observe(element);
    return () => observer.disconnect();
  }, [grid]);
  return twoColumns;
}

export function Inbox() {
  // Actions leads in the main column; stacked, Projects leads. The sections are rendered in the
  // order they are shown, so the Tab order matches what is on screen, and their keys keep each one's
  // state across the switch.
  const sections = [<InboxSection key="inbox" />, <ProjectsSection key="projects" />];
  const grid = useRef<HTMLDivElement>(null);
  const twoColumns = useTwoColumns(grid);
  return (
    // Two columns once the page's width holds the 368px Projects card, the 32px gap, the header's
    // 26px gutter on both sides and an Actions card at least as wide as Projects: 51.25rem
    // (`MENU_ROOM` in menu.tsx adds the expanded menu to this). The Projects card ends on the
    // header's right edge; beside the menu (lg) the Actions card sits 16px off it.
    <div
      ref={grid}
      className="grid grid-cols-1 gap-y-[calc(var(--spacing-band)*2)] px-padx pt-4 pb-band lg:pl-4 @[51.25rem]:grid-cols-[minmax(0,1fr)_23rem] @[51.25rem]:gap-x-8"
    >
      {twoColumns ? sections : [...sections].reverse()}
    </div>
  );
}
