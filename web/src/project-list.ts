import { useCallback, useEffect, useState } from "react";

// The projects endpoint sets no read deadline of its own, so a read still open at 15 seconds has
// stalled: it ends in the error state, or as a stale read, rather than reading forever.
const READ_DEADLINE_MS = 15_000;

export type PhaseProgress = {
  name: string;
  done: number;
  total: number;
};

type PhaseTicket = {
  id: string;
  title: string;
  stage: string;
};

export type PhaseSnapshot = {
  phases: PhaseProgress[];
  current: string;
  in_flight: PhaseTicket[];
  queued: number;
  queued_blocked: number;
  eligible: string[];
  eligible_blocked: number;
  unreadable: boolean;
  reason: string;
  at: string;
};

export type Project = {
  repository: string;
  linear_project: string;
  added_at: string;
  paused: boolean;
  reach: { status: string; reason?: string };
  snapshot: PhaseSnapshot | null;
  queue: { in_flight: number; held: number };
  last_activity?: string;
  start_requested_at?: string;
  start_refusal?: string;
};

type ProjectsResponse = {
  total: number;
  projects: Project[];
};

export type ProjectsState = {
  status: "loading" | "error" | "ready";
  total: number;
  projects: Project[];
  // When the list was read, so every card's "ago" is against one clock.
  readAt: number;
  // A re-read failed while an earlier read is on screen.
  stale: boolean;
};

export type ProjectsRead = {
  state: ProjectsState;
  // A read is open: the first one, or a re-read that leaves the cards on screen meanwhile.
  fetching: boolean;
  refresh: () => void;
  retry: () => void;
};

// displayName is how the design names a project: the part of owner/name after the slash, or the whole
// name while another listed project shares that part.
export function displayName(repository: string, projects: readonly Project[]): string {
  const bare = (name: string) => name.slice(name.lastIndexOf("/") + 1);
  const shared = projects.some(
    (other) =>
      other.repository !== repository &&
      bare(other.repository).toLowerCase() === bare(repository).toLowerCase(),
  );
  return shared ? repository : bare(repository);
}

function isProjects(body: unknown): body is ProjectsResponse {
  return (
    typeof body === "object" &&
    body !== null &&
    "total" in body &&
    typeof body.total === "number" &&
    "projects" in body &&
    Array.isArray(body.projects)
  );
}

// useProjects reads the list. refresh re-reads in place; retry shows the
// loading panel again first, for a read that failed.
export function useProjects(): ProjectsRead {
  const [state, setState] = useState<ProjectsState>({
    status: "loading",
    total: 0,
    projects: [],
    readAt: 0,
    stale: false,
  });
  const [attempt, setAttempt] = useState(0);
  const [fetching, setFetching] = useState(true);

  useEffect(() => {
    const controller = new AbortController();

    async function read() {
      const response = await fetch("/api/projects", {
        signal: AbortSignal.any([controller.signal, AbortSignal.timeout(READ_DEADLINE_MS)]),
      });
      if (!response.ok) {
        throw new Error(`the projects endpoint answered ${response.status}`);
      }
      const body: unknown = await response.json();
      if (!isProjects(body)) {
        throw new Error("the projects endpoint answered no project list");
      }
      if (controller.signal.aborted) {
        return;
      }
      setState({
        status: "ready",
        total: body.total,
        projects: body.projects,
        readAt: Date.now(),
        stale: false,
      });
      setFetching(false);
    }

    read().catch((err: unknown) => {
      if (controller.signal.aborted) {
        return;
      }
      console.error("projectsNotRead", err);
      setState((previous) =>
        previous.status === "ready"
          ? { ...previous, stale: true }
          : { status: "error", total: 0, projects: [], readAt: 0, stale: false },
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
    setState({ status: "loading", total: 0, projects: [], readAt: 0, stale: false });
    setFetching(true);
    setAttempt((n) => n + 1);
  }, []);

  return { state, fetching, refresh, retry };
}
