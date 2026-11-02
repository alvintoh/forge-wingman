import { Link, useLocation } from "@tanstack/react-router";
import { Account } from "./account";
import { attentionCount, useInbox } from "./inbox-context";
import type { Inbox } from "./inbox";

type PageHeaderProps = {
  // The menu button, under lg only: from lg the menu's own foot folds it.
  menu: React.ReactNode;
};

/**
 * PageHeader is every page's header: the menu button under lg, the app's icon and name
 * (one link to Overview), with the signed-in account at the right end of the title row, and under
 * it the line saying how many runs need the owner. The name is the page's heading only on the inbox,
 * which has no title of its own; every other page keeps its own h1 below it.
 */
export function PageHeader({ menu }: PageHeaderProps) {
  const { state } = useInbox();
  const onInbox = useLocation({ select: (location) => location.pathname === "/" });
  const Title = onInbox ? "h1" : "p";
  return (
    <header className="px-padx pt-6">
      {/* The row is the account menu's anchor. Its 44px height is the avatar's hit
          area, which centres the tile and the icon on the row. The drawn header
          centres them on the title's capitals, which sit 0.026em below the row's
          middle in this font, so the title is lifted by that much. Under lg the
          menu button's 32px tile takes the icon's place (the drawer shows the
          icon), mirroring the avatar tile at the row's other end. The name is the 22px
          title on every phone width and the 34px hero from md (the tablet, 768px) up
          (the hero is too wide beside the menu button and the avatar on a phone). The
          title's line box is 1.25 tall, not 1: truncate clips at that box, and a
          1.0 box cut the g's descenders off. */}
      <div className="relative flex h-11 items-center justify-between gap-3">
        <div className="flex min-w-0 items-center text-ink">
          {menu}
          <Link to="/" className="flex h-11 min-w-0 items-center">
            {menu === null && (
              <img src="/icon.svg" alt="" width={32} height={32} className="size-8 shrink-0" />
            )}
            <Title
              className={`relative -top-[0.026em] truncate ${menu === null ? "ml-3" : "ml-1.5"} font-sans text-title leading-tight font-semibold md:text-hero`}
            >
              Forge Wingman
            </Title>
          </Link>
        </div>
        <Account />
      </div>
      <AttentionLine state={state} />
      {/* The drawing puts the line's baseline 44px below the row's centre and the rule 26px below
          that baseline. In a 24px box, 17px IBM Plex Sans (ascent 1.025em, descent 0.275em) sits
          its baseline 18.4px down, so 4px above the box and 20px below it land both within half
          a pixel; the rule moves down by exactly the box's 24px. */}
      <div className="mt-5 border-b border-hair" />
    </header>
  );
}

/**
 * AttentionLine says how many runs wait on the owner, from the same count as the menu's Overview
 * badge. It stays empty, at its height, until the first read lands, so the rule never moves.
 * Not a live region: the Overview announces the inbox's state itself.
 */
export function AttentionLine({ state }: { state: Inbox["state"] }) {
  const count = attentionCount(state);
  return (
    <p className="mt-1 h-6 truncate text-head leading-6 text-ink2">
      {state.status === "error" && "The inbox could not be read."}
      {count === 0 && "Nothing needs your attention"}
      {count !== null && count > 0 && (
        <>
          {/* The space is 4px at this size; with the margin the gap is the drawing's 8px. */}
          <span className="mr-1 font-semibold text-stop">{count}</span>{" "}
          {count === 1 ? "run needs" : "runs need"} your attention
        </>
      )}
    </p>
  );
}
