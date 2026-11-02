import { createContext, useContext } from "react";
import type { Inbox } from "./inbox";

/** The inbox list, its merges, retries and cancels, held by InboxProvider for every screen. */
export const InboxContext = createContext<Inbox | null>(null);

/** useInbox reads the inbox list from the nearest InboxProvider. */
export function useInbox(): Inbox {
  const inbox = useContext(InboxContext);
  if (inbox === null) {
    throw new Error("useInbox is used outside an InboxProvider");
  }
  return inbox;
}

/**
 * attentionCount is how many runs wait on the owner, the one count the menu's Overview badge and
 * the header's line both show: null until a read has landed, or when none ever did.
 */
export function attentionCount(state: Inbox["state"]): number | null {
  return state.status === "ready" ? state.runs.length : null;
}
