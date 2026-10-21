import { Link } from "@tanstack/react-router";
import { useCallback, useEffect, useId, useRef, useState } from "react";

type PhaseProgress = {
  name: string;
  done: number;
  total: number;
};

type PhaseTicket = {
  id: string;
  title: string;
  stage: string;
};

type PhaseSnapshot = {
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

type Project = {
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

type ProjectsState = {
  status: "loading" | "error" | "ready";
  total: number;
  projects: Project[];
  // When the list was read, so every card's "ago" is against one clock.
  readAt: number;
};

// The section on the inbox shows this many projects, then a link to the rest.
const SECTION_PROJECTS = 3;

const REACH_REACHABLE = "reachable";
const REACH_CHECKING = "checking";

// The refusals the dispatcher records for a start it did not carry out.
const refusalWords: Record<string, string> = {
  "phase-moved": "The last start was not carried out: the phase moved on.",
  "linear-unreadable": "The last start was not carried out: Linear could not be read.",
  "no-eligible-ticket": "The last start was not carried out: no ticket was ready to start.",
  "repository-unreachable":
    "The last start was not carried out: the repository could not be reached.",
  "no-current-phase": "The last start was not carried out: the project has no current phase.",
  "repository-unchecked":
    "The last start was not carried out: the repository had not been checked yet.",
};

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
function useProjects() {
  const [state, setState] = useState<ProjectsState>({
    status: "loading",
    total: 0,
    projects: [],
    readAt: 0,
  });
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    const controller = new AbortController();

    async function read() {
      const response = await fetch("/api/projects", { signal: controller.signal });
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
      setState({ status: "ready", total: body.total, projects: body.projects, readAt: Date.now() });
    }

    read().catch((err: unknown) => {
      if (controller.signal.aborted) {
        return;
      }
      console.error("projectsNotRead", err);
      setState((previous) =>
        previous.status === "ready"
          ? previous
          : { status: "error", total: 0, projects: [], readAt: 0 },
      );
    });

    return () => controller.abort();
  }, [attempt]);

  const refresh = useCallback(() => setAttempt((n) => n + 1), []);
  const retry = useCallback(() => {
    setState({ status: "loading", total: 0, projects: [], readAt: 0 });
    setAttempt((n) => n + 1);
  }, []);

  return { state, refresh, retry };
}

function ago(iso: string, now: number): string {
  const minutes = Math.max(0, Math.floor((now - Date.parse(iso)) / 60_000));
  if (minutes < 1) {
    return "just now";
  }
  if (minutes < 60) {
    return `${minutes}m ago`;
  }
  const hours = Math.floor(minutes / 60);
  return hours < 24 ? `${hours}h ago` : `${Math.floor(hours / 24)}d ago`;
}

function plural(n: number, one: string, many: string): string {
  return `${n} ${n === 1 ? one : many}`;
}

type StatePill = "CHECKING" | "STOPPING" | "STOPPED" | "RUNNING" | null;

const pillTone: Record<Exclude<StatePill, null>, string> = {
  CHECKING: "text-muted",
  STOPPING: "text-defer",
  STOPPED: "text-stop",
  RUNNING: "text-ok",
};

// The view of one project: derived here from the list's fields alone.
type ProjectView = {
  pill: StatePill;
  readable: PhaseSnapshot | null;
  phase: PhaseProgress | null;
  lines: string[];
  working: boolean;
  startable: boolean;
  notice: string | null;
  startDisabled: boolean;
};

function describe(project: Project, now: number): ProjectView {
  const { paused, queue, reach } = project;
  const snapshot = project.snapshot;
  const readable = snapshot !== null && !snapshot.unreadable ? snapshot : null;
  const queued = readable?.queued ?? 0;
  const working = queue.in_flight > 0 || queued > 0;

  let pill: StatePill = null;
  if (reach.status === REACH_CHECKING) {
    pill = "CHECKING";
  } else if (paused) {
    pill = queue.in_flight > 0 ? "STOPPING" : "STOPPED";
  } else if (working) {
    pill = "RUNNING";
  }

  const phases = readable?.phases ?? [];
  const index = phases.findIndex((p) => p.name === readable?.current);
  const phase = index >= 0 ? (phases[index] ?? null) : null;
  const activity = project.last_activity
    ? `active ${ago(project.last_activity, now)}`
    : `added ${ago(project.added_at, now)}`;

  const lines: string[] = [];
  if (readable !== null && phases.length === 0) {
    lines.push(`No phases yet; ${activity}`);
  } else if (readable !== null) {
    const later = index >= 0 ? phases.length - 1 - index : 0;
    const locked =
      later === 0
        ? null
        : later === 1
          ? `phase ${phases.length} locked`
          : `phases ${index + 2}-${phases.length} locked`;
    if (pill === "STOPPING") {
      lines.push("No new tickets start", `${queue.in_flight} finishing, ${queue.held} queued held`);
    } else {
      if (phase !== null) {
        lines.push(`${phase.done} of ${phase.total} tickets done`);
      }
      if (pill === "STOPPED" && queue.held > 0) {
        lines.push(`${queue.held} queued, held until resumed`);
      } else if (working) {
        lines.push(`${queue.in_flight} in flight, ${queued} queued`);
      } else if (locked !== null) {
        lines.push(locked);
      }
    }
    lines.push(activity);
  } else {
    lines.push(activity);
  }

  const idle =
    readable !== null &&
    !paused &&
    queue.in_flight === 0 &&
    queue.held === 0 &&
    queued === 0 &&
    readable.in_flight.length === 0 &&
    readable.eligible.length > 0 &&
    !project.start_requested_at;
  const startable = idle && reach.status === REACH_REACHABLE;

  let notice: string | null = null;
  if (project.start_requested_at) {
    notice = "Start requested, delegates on the next poll.";
  } else if (reach.status === REACH_CHECKING) {
    notice = "Checking on the next poll.";
  } else if (reach.status !== REACH_REACHABLE) {
    notice = `The dispatcher's token cannot read ${project.repository}. Check its access on GitHub to start a phase.`;
  } else if (snapshot === null) {
    notice = "The phases have not been read yet.";
  } else if (project.start_refusal) {
    notice = refusalWords[project.start_refusal] ?? "The last start was not carried out.";
  }

  return {
    pill,
    readable,
    phase,
    lines,
    working,
    startable,
    notice,
    startDisabled: idle && !startable,
  };
}

function percent(phase: PhaseProgress): number {
  return phase.total > 0 ? Math.round((phase.done / phase.total) * 100) : 0;
}

type PillButtonProps = React.ComponentProps<"button"> & { label: string };

// A small outlined pill with a 44px hit area around it.
function PillButton({ label, ...rest }: PillButtonProps) {
  return (
    <button
      type="button"
      className="group inline-flex min-h-11 min-w-11 cursor-pointer items-center justify-center disabled:cursor-not-allowed"
      {...rest}
    >
      <span className="inline-flex h-8 min-w-24 items-center justify-center rounded-full border border-accent px-5 font-mono text-micro font-semibold text-accent group-disabled:border-muted group-disabled:text-muted">
        {label}
      </span>
    </button>
  );
}

type CardProps = {
  project: Project;
  now: number;
  refresh: () => void;
};

function ProjectCard({ project, now, refresh }: CardProps) {
  const view = describe(project, now);
  const { readable, phase } = view;
  const detailId = useId();
  const noticeId = useId();
  const dialogRef = useRef<HTMLDialogElement>(null);
  const [open, setOpen] = useState(false);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState(false);
  const [moved, setMoved] = useState<string | null>(null);

  // start posts the phase this card showed; the server checks it is still the
  // current one, and a refusal leaves the list as it was.
  async function start() {
    if (readable === null) {
      return;
    }
    setSaving(true);
    setSaveError(false);
    setMoved(null);
    try {
      const response = await fetch("/api/projects/start", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ repository: project.repository, phase: readable.current }),
      });
      if (response.status === 409) {
        setMoved(await response.text());
      } else if (!response.ok) {
        throw new Error(`the start endpoint answered ${response.status}`);
      }
      dialogRef.current?.close();
      refresh();
    } catch (err: unknown) {
      console.error("projectStartNotRequested", err);
      setSaveError(true);
    } finally {
      setSaving(false);
    }
  }

  const eligible = readable?.eligible.length ?? 0;
  const blocked = readable?.eligible_blocked ?? 0;

  return (
    <li className="border border-hair bg-card p-4">
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-baseline gap-x-3">
            <h3 className="break-words text-body font-semibold text-ink">{project.repository}</h3>
            {view.pill !== null && (
              <span className={`font-mono text-micro font-semibold ${pillTone[view.pill]}`}>
                {view.pill}
              </span>
            )}
          </div>
          {readable !== null && phase !== null && (
            <div className="mt-2 flex items-baseline justify-between gap-4 text-label text-ink">
              <span className="break-words">{phase.name}</span>
              <span>{percent(phase)}%</span>
            </div>
          )}
          {phase !== null && (
            <div
              role="progressbar"
              aria-label={`${phase.name} in ${project.repository}`}
              aria-valuemin={0}
              aria-valuemax={100}
              aria-valuenow={percent(phase)}
              className="mt-2 h-1 bg-track"
            >
              <div className="h-full bg-accent" style={{ width: `${percent(phase)}%` }} />
            </div>
          )}
          <div className="mt-3 text-cap text-ink2">
            {view.lines.map((line) => (
              <p key={line}>{line}</p>
            ))}
          </div>
        </div>
        {view.startable || view.startDisabled ? (
          <PillButton
            label="START"
            disabled={view.startDisabled}
            aria-describedby={view.startDisabled ? noticeId : undefined}
            aria-label={`Start ${readable?.current ?? "the phase"} in ${project.repository}`}
            onClick={() => {
              setSaveError(false);
              dialogRef.current?.showModal();
            }}
          />
        ) : (
          view.working && (
            <PillButton
              label={open ? "HIDE" : "DETAILS"}
              aria-expanded={open}
              aria-controls={detailId}
              aria-label={`${open ? "Hide" : "Details:"} what is running in ${project.repository}`}
              onClick={() => setOpen((v) => !v)}
            />
          )
        )}
      </div>

      {project.snapshot?.unreadable && (
        <div className="mt-3 flex items-center justify-between gap-4">
          <p role="status" className="text-cap text-ink2">
            The phases could not be read at {new Date(project.snapshot.at).toLocaleTimeString()}.
          </p>
          <PillButton label="TRY AGAIN" onClick={refresh} />
        </div>
      )}
      {view.notice !== null && (
        <p id={noticeId} className="mt-3 text-cap text-ink2">
          {view.notice}
        </p>
      )}
      {moved !== null && (
        <p role="alert" className="mt-3 text-cap text-defer">
          The start was not requested: {moved}.
        </p>
      )}

      {readable !== null && open && (
        <div id={detailId} className="mt-4 border-t border-hair pt-3">
          <h4 className="font-mono text-label font-semibold text-ink2">IN FLIGHT NOW</h4>
          {readable.in_flight.length === 0 ? (
            <p className="mt-2 text-cap text-ink2">Nothing is running.</p>
          ) : (
            <ul className="mt-2 flex flex-col gap-2">
              {readable.in_flight.map((ticket) => (
                <li key={ticket.id} className="flex items-baseline gap-3 text-label">
                  <span className="font-mono text-ink2">{ticket.id}</span>
                  <span className="min-w-0 flex-1 break-words text-ink">{ticket.title}</span>
                  <span className="text-cap text-ink2">{ticket.stage}</span>
                </li>
              ))}
            </ul>
          )}
          <p className="mt-3 text-cap text-ink2">
            {readable.queued} queued, {readable.queued_blocked} of them wait on a blocker
          </p>
        </div>
      )}

      {readable !== null && (
        <dialog
          ref={dialogRef}
          aria-labelledby={`${detailId}-title`}
          className="m-auto max-h-dvh w-full max-w-[min(var(--container-phone),calc(100dvw-2rem))] overflow-auto border border-hair bg-card p-6 text-ink backdrop:bg-ink/40"
        >
          <h2 id={`${detailId}-title`} className="text-head font-semibold">
            Start {readable.current} in {project.repository}?
          </h2>
          <p className="mt-3 text-body text-ink2">
            Delegates {plural(eligible, "ticket", "tickets")} to the runner. {blocked} wait on a
            blocker; the rest start as the run limits allow.
          </p>
          {saveError && (
            <p role="alert" className="mt-3 text-body text-stop">
              The start was not requested. Nothing changed; try again.
            </p>
          )}
          <div className="mt-4 flex justify-end gap-3">
            <PillButton label="BACK" onClick={() => dialogRef.current?.close()} />
            <PillButton label="START" disabled={saving} onClick={start} />
          </div>
        </dialog>
      )}
    </li>
  );
}

type ListProps = {
  projects: Project[];
  now: number;
  refresh: () => void;
};

function ProjectList({ projects, now, refresh }: ListProps) {
  return (
    <ul className="mt-4 flex flex-col gap-4">
      {projects.map((project) => (
        <ProjectCard key={project.repository} project={project} now={now} refresh={refresh} />
      ))}
    </ul>
  );
}

type PanelsProps = {
  state: ProjectsState;
  retry: () => void;
  children: React.ReactNode;
};

// The loading, error and empty panels both views share.
function ProjectsPanels({ state, retry, children }: PanelsProps) {
  if (state.status === "loading") {
    return (
      <p role="status" className="mt-4 text-body">
        Reading the projects…
      </p>
    );
  }
  if (state.status === "error") {
    return (
      <div className="mt-4">
        <p role="alert" className="text-body">
          The projects could not be read.
        </p>
        <PillButton label="TRY AGAIN" onClick={retry} />
      </div>
    );
  }
  if (state.projects.length === 0) {
    return <p className="mt-4 text-body">No project is listed.</p>;
  }
  return children;
}

// ProjectsSection is the inbox's PROJECTS section: the newest three, then a
// link to the rest.
export function ProjectsSection() {
  const { state, refresh, retry } = useProjects();
  return (
    <section
      aria-labelledby="projects-heading"
      className="mx-auto max-w-desk px-padx py-band md:px-pad"
    >
      <h2 id="projects-heading" className="font-mono text-label font-semibold text-ink2">
        PROJECTS
      </h2>
      <ProjectsPanels state={state} retry={retry}>
        <ProjectList
          projects={state.projects.slice(0, SECTION_PROJECTS)}
          now={state.readAt}
          refresh={refresh}
        />
        {state.total > SECTION_PROJECTS && (
          <Link
            to="/projects"
            className="mt-2 inline-flex min-h-11 min-w-11 items-center text-label text-accent"
          >
            See all {state.total} projects
          </Link>
        )}
      </ProjectsPanels>
    </section>
  );
}

// Projects lists every project.
export function Projects() {
  const { state, refresh, retry } = useProjects();
  return (
    <section className="mx-auto max-w-desk px-padx py-band md:px-pad">
      <h1 className="text-title font-semibold text-ink">Projects</h1>
      <ProjectsPanels state={state} retry={retry}>
        <ProjectList projects={state.projects} now={state.readAt} refresh={refresh} />
      </ProjectsPanels>
    </section>
  );
}
