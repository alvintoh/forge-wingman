import { useEffect, useId, useRef, useState } from "react";
import { ActionButton } from "./action-button";

// The me endpoint's own shape: the address the request is signed in as.
type Me = {
  email: string;
};

type MeState = { status: "loading" } | { status: "error" } | { status: "ready"; email: string };

// IAP clears its own session cookie on this query and sends the browser back
// through Google's sign-in, which is how an account is switched.
const CLEAR_LOGIN_URL = "/?gcp-iap-mode=CLEAR_LOGIN_COOKIE";

// The public page that signing out leaves on.
const LANDING_URL = "https://forge-wingman.web.app/";

// Signing out clears the session behind a request, then opens the landing
// page. IAP answers a clear with a redirect; any other answer, or none, runs
// the whole navigation through IAP's own sign-in instead, so the visitor is
// never sent away still signed in.
async function signOut() {
  try {
    const response = await fetch(CLEAR_LOGIN_URL, {
      redirect: "manual",
      credentials: "same-origin",
      cache: "no-store",
    });
    if (response.type !== "opaqueredirect") {
      throw new Error(`the clear-login request answered ${response.status}`);
    }
  } catch (err) {
    console.error("signOutNotCleared", err);
    window.location.assign(CLEAR_LOGIN_URL);
    return;
  }
  window.location.assign(LANDING_URL);
}

function switchAccount() {
  window.location.assign(CLEAR_LOGIN_URL);
}

const ACCOUNT_ACTIONS = [
  { key: "switch", label: "SWITCH ACCOUNT", run: switchAccount },
  { key: "sign-out", label: "SIGN OUT", run: signOut },
] as const;

function isMe(body: unknown): body is Me {
  return (
    typeof body === "object" && body !== null && "email" in body && typeof body.email === "string"
  );
}

// initials are the first two letters of the address's local part in capitals,
// the whole of what IAP gives to draw a person from.
function initials(email: string): string {
  return email.split("@")[0].slice(0, 2).toUpperCase();
}

// useMe reads once who IAP signed in.
function useMe(): MeState {
  const [state, setState] = useState<MeState>({ status: "loading" });

  useEffect(() => {
    const controller = new AbortController();

    async function read() {
      const response = await fetch("/api/me", { signal: controller.signal });
      if (!response.ok) {
        throw new Error(`the me endpoint answered ${response.status}`);
      }
      const body: unknown = await response.json();
      if (!isMe(body)) {
        throw new Error("the me endpoint answered no account");
      }
      if (controller.signal.aborted) {
        return;
      }
      setState({ status: "ready", email: body.email });
    }

    read().catch((err: unknown) => {
      if (controller.signal.aborted) {
        return;
      }
      console.error("meNotRead", err);
      setState({ status: "error" });
    });

    return () => controller.abort();
  }, []);

  return state;
}

/**
 * Account is the signed-in account's avatar and the menu it opens. It sits in
 * the header's title row, which must be positioned: the menu is placed from it.
 * Until the account is read, or when it cannot be, it renders nothing, since
 * neither state is an account to describe.
 */
export function Account() {
  const me = useMe();
  return me.status === "ready" ? <AccountMenu email={me.email} /> : null;
}

type AccountMenuProps = {
  email: string;
};

// AccountMenu is the avatar button and the popover it discloses (the APG
// disclosure pattern): opening moves focus into the menu, and Escape or a press
// outside closes it and returns focus to the avatar.
function AccountMenu({ email }: AccountMenuProps) {
  const [open, setOpen] = useState(false);
  const menuId = useId();
  const buttonRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const firstActionRef = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    if (!open) {
      return;
    }
    firstActionRef.current?.focus();

    function close() {
      setOpen(false);
      buttonRef.current?.focus();
    }
    function onKeyDown(event: KeyboardEvent) {
      if (event.key === "Escape") {
        close();
      }
    }
    // On click, not pointerdown: the press's own mousedown blurs to the body
    // after a pointerdown handler runs, which would undo the return of focus.
    function onClick(event: MouseEvent) {
      const target = event.target;
      if (!(target instanceof Node)) {
        return;
      }
      if (menuRef.current?.contains(target) || buttonRef.current?.contains(target)) {
        return;
      }
      close();
    }

    document.addEventListener("keydown", onKeyDown);
    document.addEventListener("click", onClick);
    return () => {
      document.removeEventListener("keydown", onKeyDown);
      document.removeEventListener("click", onClick);
    };
  }, [open]);

  return (
    <>
      {/* The hit area overhangs the content edge by 6px so the 32px tile, not
          the 44px target, sits on it, as the drawn tile does. */}
      <button
        ref={buttonRef}
        type="button"
        aria-label={`Account, signed in as ${email}`}
        aria-expanded={open}
        aria-controls={menuId}
        onClick={() => setOpen((was) => !was)}
        className="-mr-1.5 inline-flex size-11 cursor-pointer items-center justify-center"
      >
        <span className="inline-flex size-8 items-center justify-center rounded-tile bg-ink2 font-sans text-micro font-semibold tracking-[0.4px] text-card">
          {initials(email)}
        </span>
      </button>
      {/* Placed from the header's title row: 8px under the tile, whose foot is
          16px below the row's middle. The phone takes the page width inside the
          16px gutter; from md the menu is 372 wide, its right edge on the tile's. */}
      <div
        ref={menuRef}
        id={menuId}
        role="group"
        aria-label="Account"
        hidden={!open}
        className="absolute top-[calc(50%+24px)] right-[calc(1rem-var(--spacing-padx))] left-[calc(1rem-var(--spacing-padx))] z-20 rounded-card border border-hair bg-card px-padx text-left shadow-[0_8px_28px_color-mix(in_srgb,var(--color-ink)_16%,transparent)] md:right-0 md:left-auto md:w-[372px]"
      >
        <p className="pt-5 font-mono text-body leading-5 font-semibold [overflow-wrap:anywhere] text-ink">
          {email}
        </p>
        <p className="mt-1.5 text-label leading-[18px] text-ink2">
          Sign-in is Google's, through IAP. Only the allowed account gets in.
        </p>
        {/* The dialogs' button, stacked at the content's full width, 12px apart. */}
        <div className="mt-[22px] grid gap-3 border-t border-hair pt-4 pb-[22px]">
          {ACCOUNT_ACTIONS.map((action, i) => (
            <ActionButton
              key={action.key}
              ref={i === 0 ? firstActionRef : undefined}
              kind="secondary"
              label={action.label}
              onClick={action.run}
            />
          ))}
        </div>
      </div>
    </>
  );
}
