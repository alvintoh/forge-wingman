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
