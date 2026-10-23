import { Link, useLocation } from "@tanstack/react-router";
import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import { createPortal } from "react-dom";
import { PageHeader } from "./header";
import { useInbox } from "./inbox-context";

// The id of the menu, which the drawer's menu button and the column's collapse button control.
const MENU_ID = "app-menu";

// From Tailwind's lg the menu is a column beside the page; under it, a drawer from the menu button.
const MENU_COLUMN = "(min-width: 64rem)";

// The width from which the expanded menu leaves the Overview its two columns: their 51.25rem (the
// container query in pages.tsx), the 15rem menu and 1rem for a scrollbar. Narrower, a menu with no
// saved choice starts as the rail, as a sidebar collapses in a narrow window, so the columns never
// stack between two widths where they sit side by side.
const MENU_ROOM = "(min-width: 67.25rem)";

const MENU_WIDTH_KEY = "forge-wingman.menu-width";

type MenuWidth = "expanded" | "rail";

type Screen = {
  to: "/" | "/projects" | "/stats" | "/autonomy" | "/peak" | "/providers";
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
  {
    to: "/projects",
    label: "Projects",
    icon: "M6 4H18A2 2 0 0 1 20 6V8A2 2 0 0 1 18 10H6A2 2 0 0 1 4 8V6A2 2 0 0 1 6 4ZM6 14H18A2 2 0 0 1 20 16V18A2 2 0 0 1 18 20H6A2 2 0 0 1 4 18V16A2 2 0 0 1 6 14Z",
  },
  { to: "/stats", label: "Statistics", icon: "M4 20V10M10 20V4M16 20v-8M22 20H2" },
];

const SETTINGS_SCREENS: readonly Screen[] = [
  { to: "/autonomy", label: "Autonomy", icon: "M20 12a8 8 0 1 1-16 0a8 8 0 1 1 16 0ZM12 8v4l3 2" },
  { to: "/peak", label: "Peak hours", icon: "M3 18l6-8 4 5 3-4 5 7z" },
  {
    to: "/providers",
    label: "Providers",
    icon: "M5 4H19A2 2 0 0 1 21 6V8A2 2 0 0 1 19 10H5A2 2 0 0 1 3 8V6A2 2 0 0 1 5 4ZM5 14H19A2 2 0 0 1 21 16V18A2 2 0 0 1 19 20H5A2 2 0 0 1 3 18V16A2 2 0 0 1 5 14ZM7 7h.01M7 17h.01",
  },
];

// A screen is current only on its own path, so a run's page marks no item.
const EXACT = { exact: true } as const;

function useMedia(query: string) {
  return useSyncExternalStore(
    (notify) => {
      const list = window.matchMedia(query);
      list.addEventListener("change", notify);
      return () => list.removeEventListener("change", notify);
    },
    () => window.matchMedia(query).matches,
    () => false,
  );
}

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
 * AppShell is every page's frame: the menu, and beside it the header and the page in one box. From
 * lg the menu is a full-height column, folded to a rail of icons and back by the row at its foot, and
 * the choice is kept in this browser; under lg it is a drawer from the header's menu button.
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
      className="-ml-1.5 inline-flex size-11 shrink-0 cursor-pointer items-center justify-center text-ink2"
    >
      <span className="grid size-8 place-items-center rounded-tile border border-muted">
        <svg
          aria-hidden="true"
          width="18"
          height="12"
          viewBox="-1 -1 18 12"
          fill="none"
          stroke="currentColor"
          strokeWidth="2"
          strokeLinecap="round"
        >
          <path d="M0 0h16M0 5h16M0 10h16" />
        </svg>
      </span>
    </button>
  );

  return (
    <div className="flex min-h-dvh">
      {column && (
        // The column runs the page's full height and its inner box sticks to the viewport, so the
        // collapse row always sits at the viewport's foot and the items scroll above it.
        <div className={`shrink-0 border-r border-hair ${rail ? "w-18" : "w-60"}`}>
          <div className="sticky top-0 flex h-dvh flex-col">
            <div className="min-h-0 flex-1 overflow-y-auto">
              <Menu rail={rail} />
            </div>
            <div className={`pb-3 ${rail ? "px-3.5" : "px-3"}`}>
              <div className={`border-t border-hair ${rail ? "" : "mx-2"}`} />
              <div className="pt-3">
                <CollapseButton rail={rail} onToggle={toggleWidth} />
              </div>
            </div>
          </div>
        </div>
      )}
      {/* The header and the page share one width, centred beside the menu, so the avatar stands
          over the page's right edge and every page starts under the header's left edge. */}
      <div className="min-w-0 flex-1">
        <div className="mx-auto max-w-desk">
          <PageHeader menu={button} />
          <main className="@container">{children}</main>
        </div>
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
