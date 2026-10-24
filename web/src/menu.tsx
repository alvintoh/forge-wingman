import { Link, useLocation } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { PageHeader } from "./header";
import { useInbox } from "./inbox-context";
import { useMedia } from "./use-media";

// The id of the menu, which the drawer's menu button and the column's collapse button control.
const MENU_ID = "app-menu";

// From Tailwind's lg the menu is a column beside the page; under it, a drawer from the menu button.
const MENU_COLUMN = "(min-width: 64rem)";

// The width from which the expanded menu leaves the Overview its two columns: their 51.25rem (the
// container query in pages/overview.tsx), the 15rem menu and 1rem for a scrollbar. Narrower, a menu
// with no saved choice starts as the rail, as a sidebar collapses in a narrow window, so the columns
// never stack between two widths where they sit side by side.
const MENU_ROOM = "(min-width: 67.25rem)";

const MENU_WIDTH_KEY = "forge-wingman.menu-width";

type MenuWidth = "expanded" | "rail";

type Screen = {
  to: "/" | "/stats" | "/autonomy" | "/config";
  label: string;
  // The icon's path in a 24px box, stroked 2px and drawn at 22px.
  icon: string;
};

const RUN_SCREENS: readonly Screen[] = [
  {
    to: "/",
    label: "Overview",
    icon: "M6 5H18A3 3 0 0 1 21 8V16A3 3 0 0 1 18 19H6A3 3 0 0 1 3 16V8A3 3 0 0 1 6 5ZM3 9h18",
  },
  { to: "/stats", label: "Statistics", icon: "M4 20V10M10 20V4M16 20v-8M22 20H2" },
];

const SETTINGS_SCREENS: readonly Screen[] = [
  { to: "/autonomy", label: "Autonomy", icon: "M20 12a8 8 0 1 1-16 0a8 8 0 1 1 16 0ZM12 8v4l3 2" },
  {
    to: "/config",
    label: "Configuration",
    // Lucide's settings gear (ISC), the cog and its centre ring.
    icon: "M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2ZM15 12a3 3 0 1 1-6 0a3 3 0 1 1 6 0Z",
  },
];

// A screen is current only on its own path, so a run's page marks no item.
const EXACT = { exact: true } as const;

// The saved choice, or null when the owner has not made one here.
function readMenuWidth(): MenuWidth | null {
  try {
    const saved = window.localStorage.getItem(MENU_WIDTH_KEY);
    return saved === "rail" || saved === "expanded" ? saved : null;
  } catch {
    return null;
  }
}

function writeMenuWidth(width: MenuWidth) {
  try {
    window.localStorage.setItem(MENU_WIDTH_KEY, width);
  } catch {
    // A store the browser refuses keeps the choice for this visit only.
  }
}

type AppShellProps = {
  children: React.ReactNode;
};

/**
 * AppShell is every page's frame: the header across the top, and below it the menu and the page.
 * From lg the menu is a column left of the page, folded to a rail of icons and back by the row at its
 * foot, and the choice is kept in this browser; under lg it is a drawer from the header's menu
 * button.
 */
export function AppShell({ children }: AppShellProps) {
  const column = useMedia(MENU_COLUMN);
  const room = useMedia(MENU_ROOM);
  const [saved, setSaved] = useState(readMenuWidth);
  const pathname = useLocation({ select: (location) => location.pathname });
  // The path the drawer was opened on: it is open only while that is still the path, so any
  // navigation closes it.
  const [drawerPath, setDrawerPath] = useState<string | null>(null);
  const buttonRef = useRef<HTMLButtonElement>(null);
  if (column && drawerPath !== null) {
    setDrawerPath(null);
  }
  const drawerOpen = !column && drawerPath === pathname;
  const rail = (saved ?? (room ? "expanded" : "rail")) === "rail";

  function toggleWidth() {
    const next = rail ? "expanded" : "rail";
    setSaved(next);
    writeMenuWidth(next);
  }

  function toggleDrawer() {
    setDrawerPath(drawerOpen ? null : pathname);
  }

  function closeDrawer() {
    setDrawerPath(null);
    buttonRef.current?.focus();
  }

  const button = column ? null : (
    <button
      ref={buttonRef}
      type="button"
      aria-label="Menu"
      aria-expanded={drawerOpen}
      aria-controls={MENU_ID}
      onClick={toggleDrawer}
      className="group -ml-1.5 inline-flex size-11 shrink-0 cursor-pointer items-center justify-center text-control-icon"
    >
      <span className="grid size-8 place-items-center rounded-control border border-control-edge bg-control transition-colors duration-150 group-hover:bg-control-hover">
        {/* Octicons' three-bars-16, the glyph GitHub's own menu button carries. */}
        <svg aria-hidden="true" width="16" height="16" viewBox="0 0 16 16" fill="currentColor">
          <path d="M1 2.75A.75.75 0 0 1 1.75 2h12.5a.75.75 0 0 1 0 1.5H1.75A.75.75 0 0 1 1 2.75Zm0 5A.75.75 0 0 1 1.75 7h12.5a.75.75 0 0 1 0 1.5H1.75A.75.75 0 0 1 1 7.75ZM1.75 12h12.5a.75.75 0 0 1 0 1.5H1.75a.75.75 0 0 1 0-1.5Z" />
        </svg>
      </span>
    </button>
  );

  return (
    // The whole frame is capped and centred: the header across its top, the menu below it on the
    // left and the page beside the menu, so the avatar stands over the page's right edge at every
    // width (the 15rem menu plus the 70rem page).
    <div className="mx-auto flex min-h-dvh max-w-[85rem] flex-col">
      <PageHeader menu={button} />
      <div className="flex flex-1">
        {column && (
          // The column fills the rest of the page's height; its items stick to the viewport's top
          // and its collapse row to the viewport's foot, so both stay in view however far the page
          // scrolls.
          <div className={`flex shrink-0 flex-col border-r border-hair ${rail ? "w-18" : "w-60"}`}>
            <div className="sticky top-0 max-h-[calc(100dvh-69px)] overflow-y-auto">
              <Menu rail={rail} />
            </div>
            <div className={`sticky bottom-0 mt-auto bg-ground pb-3 ${rail ? "px-3.5" : "px-3"}`}>
              <div className={`border-t border-hair ${rail ? "" : "mx-2"}`} />
              <div className="pt-3">
                <CollapseButton rail={rail} onToggle={toggleWidth} />
              </div>
            </div>
          </div>
        )}
        <main className="@container min-w-0 flex-1">{children}</main>
      </div>
      {!column && <MenuDrawer open={drawerOpen} onClose={closeDrawer} onNavigate={closeDrawer} />}
    </div>
  );
}

type CollapseButtonProps = {
  rail: boolean;
  onToggle: () => void;
};

// CollapseButton is the desktop menu's foot: it folds the menu to a rail of icons and back. On the
// rail its chevrons point the way the menu will open.
function CollapseButton({ rail, onToggle }: CollapseButtonProps) {
  return (
    <button
      type="button"
      aria-label={rail ? "Expand menu" : "Collapse menu"}
      aria-controls={MENU_ID}
      onClick={onToggle}
      className={`flex h-11 cursor-pointer items-center rounded-full text-label text-ink2 transition-colors duration-150 hover:bg-track/60 ${rail ? "w-11 justify-center" : "w-full gap-3 px-3.5"}`}
    >
      <svg
        aria-hidden="true"
        width="22"
        height="22"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth="2"
        strokeLinecap="round"
        strokeLinejoin="round"
        className={`shrink-0 ${rail ? "-scale-x-100" : ""}`}
      >
        <path d="M18 7l-5 5 5 5M11 7l-5 5 5 5" />
      </svg>
      {!rail && <span>Collapse</span>}
    </button>
  );
}

type MenuDrawerProps = {
  open: boolean;
  onClose: () => void;
  onNavigate: () => void;
};

// MenuDrawer is the menu under lg: a modal drawer from the left with the app's icon and name at its
// top, since the header shows the menu button in the icon's place. Escape, a press on the scrim and
// a navigation close it.
function MenuDrawer({ open, onClose, onNavigate }: MenuDrawerProps) {
  const dialogRef = useRef<HTMLDialogElement>(null);

  useEffect(() => {
    const dialog = dialogRef.current;
    if (dialog === null) {
      return;
    }
    if (open && !dialog.open) {
      dialog.showModal();
    }
    if (!open && dialog.open) {
      dialog.close();
    }
  }, [open]);

  // The drawer's content fills it, so a press on the dialog itself is a press on the scrim.
  function handleClick(event: React.MouseEvent<HTMLDialogElement>) {
    if (event.target === event.currentTarget) {
      event.currentTarget.close();
    }
  }

  return createPortal(
    <dialog
      ref={dialogRef}
      aria-label="Menu"
      onClose={onClose}
      onClick={handleClick}
      className="fixed inset-y-0 left-0 m-0 h-dvh max-h-none w-75 max-w-[calc(100dvw-3rem)] overflow-y-auto rounded-r-frame bg-card text-ink shadow-[0_8px_28px_color-mix(in_srgb,var(--color-ink)_16%,transparent)] backdrop:bg-ink/45"
    >
      <div className="min-h-full">
        {/* The icon's centre and the name's capitals 44px down, the rule at 76, the menu at 80. */}
        <div className="mx-padx flex h-11 items-center gap-3 pt-5.5 box-content">
          <img src="/icon.svg" alt="" width={32} height={32} className="size-8 shrink-0" />
          <p className="truncate text-body font-semibold">Forge Wingman</p>
        </div>
        <div className="mx-5 mt-2.5 border-b border-hair" />
        <div className="pt-0.75">
          <Menu rail={false} onNavigate={onNavigate} />
        </div>
      </div>
    </dialog>,
    document.body,
  );
}

type MenuProps = {
  rail: boolean;
  onNavigate?: () => void;
};

/**
 * Menu lists every top-level screen, the run screens then, past a rule, the settings. Each item is
 * a 44px row, its icon in the accent on a track pill while it is the current screen; a rail shows
 * the icons alone, each named for its screen. Only Overview carries a count: how many actions wait.
 */
function Menu({ rail, onNavigate }: MenuProps) {
  const { state } = useInbox();
  const actions = state.status === "ready" ? state.runs.length : 0;
  return (
    <nav id={MENU_ID} aria-label="Menu" className={`pt-3 pb-3 ${rail ? "px-3.5" : "px-3"}`}>
      <ul className="flex flex-col gap-1">
        {RUN_SCREENS.map((screen) => (
          <MenuItem
            key={screen.to}
            screen={screen}
            count={screen.to === "/" ? actions : 0}
            rail={rail}
            onNavigate={onNavigate}
          />
        ))}
      </ul>
      {/* 12px between the two lists' items, the rule 6px into it; the rail keeps the space alone. */}
      <div
        className={`mx-2 mt-2.5 mb-1.25 border-b ${rail ? "border-transparent" : "border-hair"}`}
      />
      <ul className="flex flex-col gap-1">
        {SETTINGS_SCREENS.map((screen) => (
          <MenuItem key={screen.to} screen={screen} count={0} rail={rail} onNavigate={onNavigate} />
        ))}
      </ul>
    </nav>
  );
}

type MenuItemProps = {
  screen: Screen;
  count: number;
  rail: boolean;
  onNavigate?: () => void;
};

function MenuItem({ screen, count, rail, onNavigate }: MenuItemProps) {
  const actions = count === 1 ? "action" : "actions";
  const name = count > 0 ? `${screen.label}, ${count} ${actions}` : screen.label;
  return (
    <li>
      <Link
        to={screen.to}
        activeOptions={EXACT}
        onClick={onNavigate}
        aria-label={name}
        title={rail ? screen.label : undefined}
        className={`group flex h-11 items-center rounded-full text-label text-ink transition-colors duration-150 hover:bg-track/60 aria-[current=page]:bg-track aria-[current=page]:font-semibold ${rail ? "w-11 justify-center" : "gap-3 px-3.5"}`}
      >
        <span className="relative shrink-0">
          <svg
            aria-hidden="true"
            width="22"
            height="22"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth="2"
            strokeLinecap="round"
            strokeLinejoin="round"
            className="block text-ink2 group-aria-[current=page]:text-accent"
          >
            <path d={screen.icon} />
          </svg>
          {/* On the rail the count rides the icon; the link's name already says it. */}
          {rail && count > 0 && (
            <span
              aria-hidden="true"
              className="absolute -top-1.5 -right-2 inline-flex h-4 min-w-4 items-center justify-center rounded-full bg-track px-1 font-mono text-micro leading-none font-semibold text-ink2 ring-2 ring-ground"
            >
              {count}
            </span>
          )}
        </span>
        {!rail && <span className="min-w-0 flex-1 truncate">{screen.label}</span>}
        {!rail && count > 0 && (
          <span aria-hidden="true" className="font-mono text-micro font-semibold text-ink2">
            {count}
          </span>
        )}
      </Link>
    </li>
  );
}
