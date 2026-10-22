import { useLocation } from "@tanstack/react-router";
import { Account } from "./account";

/**
 * PageHeader is every page's header: the app's icon and name, with the
 * signed-in account at the right end of the title row. The name is the page's
 * heading only on the inbox, which has no title of its own; every other page
 * keeps its own h1 below it.
 */
export function PageHeader() {
  const onInbox = useLocation({ select: (location) => location.pathname === "/" });
  const Title = onInbox ? "h1" : "p";
  return (
    <header className="mx-auto max-w-desk px-padx pt-6">
      {/* The row is the menu's anchor. Its 44px height is the avatar's hit
          area, which centres the tile on the title's line. The 34px title needs
          about 392px beside the icon and the avatar, so a narrower phone takes
          the 22px title rather than cutting the name short. */}
      <div className="relative flex h-11 items-center justify-between gap-3">
        <div className="flex min-w-0 items-center gap-3 text-ink">
          <img src="/icon.svg" alt="" width={32} height={32} className="size-8 shrink-0" />
          <Title className="truncate font-sans text-title leading-none min-[400px]:text-hero font-semibold">
            Forge Wingman
          </Title>
        </div>
        <Account />
      </div>
      <div className="mt-6 border-b border-hair" />
    </header>
  );
}
