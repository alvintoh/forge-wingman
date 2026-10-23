import { useCallback, useEffect, useState } from "react";
import { InboxContext } from "../inbox-context";
import { type Merges, useMerges } from "./merge-state";
import { type InboxRun, isInbox } from "./run";

type InboxState = {
  status: "loading" | "error" | "ready";
  runs: InboxRun[];
  // When the list was read, so every row's age is against one clock.
  readAt: number;
  // A re-read failed while an earlier read is on screen.
  stale: boolean;
};

type InboxRead = {
  state: InboxState;
  refresh: () => void;
  retry: () => void;
};

/** The inbox list, the ways to read it again, and the merges made from it. */
export type Inbox = InboxRead & Merges;

// useInboxRead reads the list. refresh re-reads in place; retry shows the loading panel again
// first, for a read that failed. A re-read skips the browser's copy: the list is cached for 30
// seconds, and a PR just merged must leave it.
function useInboxRead(): InboxRead {
  const [state, setState] = useState<InboxState>({
    status: "loading",
    runs: [],
    readAt: 0,
    stale: false,
  });
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    const controller = new AbortController();

    async function read() {
      const response = await fetch("/api/inbox", {
        signal: controller.signal,
        cache: attempt === 0 ? "default" : "reload",
      });
      if (!response.ok) {
        throw new Error(`the inbox endpoint answered ${response.status}`);
      }
      const body: unknown = await response.json();
      if (!isInbox(body)) {
        throw new Error("the inbox endpoint answered no run list");
      }
      if (controller.signal.aborted) {
        return;
      }
      setState({ status: "ready", runs: body.runs, readAt: Date.now(), stale: false });
    }

    read().catch((err: unknown) => {
      if (controller.signal.aborted) {
        return;
      }
      console.error("inboxNotRead", err);
      setState((previous) =>
        previous.status === "ready"
          ? { ...previous, stale: true }
          : { status: "error", runs: [], readAt: 0, stale: false },
      );
    });

    return () => controller.abort();
  }, [attempt]);

  const refresh = useCallback(() => setAttempt((n) => n + 1), []);
  const retry = useCallback(() => {
    setState({ status: "loading", runs: [], readAt: 0, stale: false });
    setAttempt((n) => n + 1);
  }, []);

  return { state, refresh, retry };
}

type InboxProviderProps = {
  children: React.ReactNode;
};

/**
 * InboxProvider reads the inbox for every screen inside it and holds the merges made from it: the
 * menu counts the actions and the Overview lists them.
 */
export function InboxProvider({ children }: InboxProviderProps) {
  const read = useInboxRead();
  const merges = useMerges(read.refresh);
  return <InboxContext value={{ ...read, ...merges }}>{children}</InboxContext>;
}
