import { useCallback, useEffect, useState } from "react";
import { isRunning, type RunningRun } from "./run";

// The server gives up on its own read at 10 seconds, so a read still open at 15 has stalled: it
// ends in the error state and its TRY AGAIN rather than reading forever.
const READ_DEADLINE_MS = 15_000;

export type RunningState = {
  status: "loading" | "error" | "ready";
  runs: RunningRun[];
  // When the list was read, so every bar measures one clock.
  readAt: number;
  // A re-read failed while an earlier read is on screen.
  stale: boolean;
};

export type RunningRead = {
  state: RunningState;
  // A read is open: the first one, or a re-read that leaves the rows on screen meanwhile.
  fetching: boolean;
  refresh: () => void;
  retry: () => void;
};

/**
 * useRunning reads the runs in flight. refresh re-reads in place; retry shows the loading panel
 * again first, for a read that failed.
 */
export function useRunning(): RunningRead {
  const [state, setState] = useState<RunningState>({
    status: "loading",
    runs: [],
    readAt: 0,
    stale: false,
  });
  const [attempt, setAttempt] = useState(0);
  const [fetching, setFetching] = useState(true);

  useEffect(() => {
    const controller = new AbortController();

    async function read() {
      const response = await fetch("/api/running", {
        signal: AbortSignal.any([controller.signal, AbortSignal.timeout(READ_DEADLINE_MS)]),
      });
      if (!response.ok) {
        throw new Error(`the running endpoint answered ${response.status}`);
      }
      const body: unknown = await response.json();
      if (!isRunning(body)) {
        throw new Error("the running endpoint answered no run list");
      }
      if (controller.signal.aborted) {
        return;
      }
      setState({ status: "ready", runs: body.runs, readAt: Date.now(), stale: false });
      setFetching(false);
    }

    read().catch((err: unknown) => {
      if (controller.signal.aborted) {
        return;
      }
      console.error("runningNotRead", err);
      setState((previous) =>
        previous.status === "ready"
          ? { ...previous, stale: true }
          : { status: "error", runs: [], readAt: 0, stale: false },
      );
      setFetching(false);
    });

    return () => controller.abort();
  }, [attempt]);

  const refresh = useCallback(() => {
    setFetching(true);
    setAttempt((n) => n + 1);
  }, []);
  const retry = useCallback(() => {
    setState({ status: "loading", runs: [], readAt: 0, stale: false });
    setFetching(true);
    setAttempt((n) => n + 1);
  }, []);

  return { state, fetching, refresh, retry };
}
