import { Link } from "@tanstack/react-router";
import { useCallback, useEffect, useId, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { ActionButton, ButtonPair } from "./action-button";
import { AddProjectDialog } from "./add-project";
import { ago } from "./ago";
import { pillSetWidth } from "./button-style";
import { SmallPill, TryAgainPill } from "./pill-button";
import { postJSON } from "./post-json";

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
  // A re-read failed while an earlier read is on screen.
  stale: boolean;
};

// The section on the inbox shows this many projects, then a link to the rest.
const SECTION_PROJECTS = 3;

const REACH_REACHABLE = "reachable";
const REACH_CHECKING = "checking";

// Every small pill on the Projects card takes RESUME's width, the set's longest label.
const PROJECT_PILL_WIDTH = pillSetWidth(["STOP", "RESUME", "START", "ADD"]);

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
    stale: false,
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
      setState({
        status: "ready",
        total: body.total,
        projects: body.projects,
        readAt: Date.now(),
        stale: false,
      });
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
    });

    return () => controller.abort();
  }, [attempt]);

  const refresh = useCallback(() => setAttempt((n) => n + 1), []);
  const retry = useCallback(() => {
    setState({ status: "loading", total: 0, projects: [], readAt: 0, stale: false });
    setAttempt((n) => n + 1);
  }, []);

  return { state, refresh, retry };
}

function plural(n: number, one: string, many: string): string {
  return `${n} ${n === 1 ? one : many}`;
}

type StatePill = "CHECKING" | "STOPPING" | "STOPPED" | "RUNNING" | null;

// The state chip's fill, as the design draws it: the word is in white on a solid pill.
const chipFill: Record<Exclude<StatePill, null>, string> = {
  CHECKING: "bg-muted",
  STOPPING: "bg-defer",
  STOPPED: "bg-muted",
  RUNNING: "bg-accent",
};

// One status line of a card. The phone card keeps only the line marked `keep` (what is moving);
// the others show from md up and sit behind Details on a phone.
type StatusLine = { text: string; keep: boolean };

// The view of one project: derived here from the list's fields alone.
type ProjectView = {
  pill: StatePill;
  readable: PhaseSnapshot | null;
  phase: PhaseProgress | null;
  lines: StatusLine[];
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
    ? `Active ${ago(project.last_activity, now)}`
    : `Added ${ago(project.added_at, now)}`;

  const lines: StatusLine[] = [];
  const line = (text: string, keep = false) => lines.push({ text, keep });
  if (readable !== null && phases.length === 0) {
    line(`No phases yet; ${activity.charAt(0).toLowerCase()}${activity.slice(1)}`, true);
  } else if (readable !== null) {
    const later = index >= 0 ? phases.length - 1 - index : 0;
    const locked =
      later === 0
        ? null
        : later === 1
          ? `Phase ${phases.length} locked`
          : `Phases ${index + 2}-${phases.length} locked`;
    if (pill === "STOPPING") {
      line("No new tickets start");
      line(`${queue.in_flight} finishing, ${queue.held} queued held`, true);
    } else {
      if (phase !== null) {
        line(`${phase.done} of ${phase.total} tickets done`);
      }
      if (pill === "STOPPED" && queue.held > 0) {
        line(`${queue.held} queued, held until resumed`, true);
      } else if (working) {
        line(`${queue.in_flight} in flight, ${queued} queued`, true);
      } else if (locked !== null) {
        line(locked, true);
      }
    }
    line(activity);
  } else {
    line(activity, true);
  }
  // A phone card always shows one status line: the last line when none is marked.
  if (!lines.some((l) => l.keep)) {
    lines.splice(-1, 1, { text: lines[lines.length - 1]?.text ?? activity, keep: true });
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
    notice = unreachableWords(project);
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
    startable,
    notice,
    startDisabled: idle && !startable,
  };
}

// unreachableWords says why the dispatcher could not read the repository, from
// the closed set of reasons it records.
function unreachableWords(project: Project): string {
  const { repository, reach } = project;
  if (reach.reason === "not-found") {
    return `${repository} was not found, or the dispatcher's token cannot see it. Check its name and access on GitHub to start a phase.`;
  }
  if (reach.reason === "forbidden" || reach.reason === "unauthorized") {
    return `The dispatcher's token cannot read ${repository}. Check its access on GitHub to start a phase.`;
  }
  return `The dispatcher could not reach ${repository}. It is checked again on the next poll.`;
}

function percent(phase: PhaseProgress): number {
  return phase.total > 0 ? Math.round((phase.done / phase.total) * 100) : 0;
}

// runsSentence and heldSentence word the stop confirm's counts, singular and plural.
function runsSentence(n: number): string {
  if (n === 0) {
    return "No run is in flight.";
  }
  return n === 1
    ? "The 1 run in flight finishes, and so does any ticket already planned."
    : `The ${n} runs in flight finish, and so does any ticket already planned.`;
}

function heldSentence(n: number): string {
  if (n === 0) {
    return "No ticket is queued.";
  }
  return n === 1
    ? "1 queued ticket stays delegated and waits until you resume."
    : `${n} queued tickets stay delegated and wait until you resume.`;
}

type EntryProps = {
  project: Project;
  now: number;
  refresh: () => void;
};

function ProjectEntry({ project, now, refresh }: EntryProps) {
  const view = describe(project, now);
  const { readable, phase } = view;
  const detailId = useId();
  const noticeId = useId();
  const dialogRef = useRef<HTMLDialogElement>(null);
  const stopRef = useRef<HTMLDialogElement>(null);
  const [open, setOpen] = useState(false);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState(false);
  const [moved, setMoved] = useState<string | null>(null);
  const [stopError, setStopError] = useState(false);
  const [resumeError, setResumeError] = useState(false);

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
      const response = await postJSON("/api/projects/start", {
        repository: project.repository,
        phase: readable.current,
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

  // pause stops or resumes the project; a failure leaves the state as it was.
  async function pause(action: "stop" | "resume") {
    setSaving(true);
    setStopError(false);
    setResumeError(false);
    try {
      const response = await postJSON(`/api/projects/${action}`, {
        repository: project.repository,
      });
      if (!response.ok) {
        throw new Error(`the ${action} endpoint answered ${response.status}`);
      }
      stopRef.current?.close();
      refresh();
    } catch (err: unknown) {
      console.error("projectPauseNotSet", action, err);
      (action === "stop" ? setStopError : setResumeError)(true);
    } finally {
      setSaving(false);
    }
  }

  const eligible = readable?.eligible.length ?? 0;
  const blocked = readable?.eligible_blocked ?? 0;

  const control =
    view.startable || view.startDisabled ? (
      <SmallPill
        kind="secondary"
        width={PROJECT_PILL_WIDTH}
        label="START"
        disabled={view.startDisabled}
        aria-describedby={view.startDisabled ? noticeId : undefined}
        aria-label={`Start ${readable?.current ?? "the phase"} in ${project.repository}`}
        onClick={() => {
          setSaveError(false);
          dialogRef.current?.showModal();
        }}
      />
    ) : view.pill === "RUNNING" ? (
      <SmallPill
        kind="secondary"
        width={PROJECT_PILL_WIDTH}
        label="STOP"
        aria-label={`Stop ${readable?.current ?? "the phase"} in ${project.repository}`}
        onClick={() => {
          setStopError(false);
          stopRef.current?.showModal();
        }}
      />
    ) : (
      (view.pill === "STOPPING" || view.pill === "STOPPED") && (
        <SmallPill
          kind="secondary"
          width={PROJECT_PILL_WIDTH}
          label="RESUME"
          disabled={saving}
          aria-label={`Resume ${readable?.current ?? "the phase"} in ${project.repository}`}
          onClick={() => pause("resume")}
        />
      )
    );

  return (
    // One project is one section of the Projects card. Its vertical rhythm is the drawing's,
    // measured from the section's top (the card's edge, or the rule above it): the pill at 22.8 and
    // the name's baseline at 42, the phase row's at 74, the bar at 84, the first status line's at
    // 108 and 16 apart, Details 24 under the last line, and the next rule (or the card's edge) 20
    // under the last baseline. Each margin below is that gap less the 16px line box around the
    // baseline (its baseline sits 12px down). pt: 22.8, less the 8px between the 44px hit area and
    // the 28px pill, less the 1px rule or card edge. pb: 20, less the 4px under a last line's
    // baseline.
    <li className="border-t border-hair pt-[13.8px] pb-4 first:border-t-0">
      <div className="min-w-0">
        <div className="min-w-0">
          <div className="flex min-h-11 items-center justify-between gap-3">
            <div className="flex min-w-0 items-center gap-2.5">
              <h3
                title={project.repository}
                className="min-w-0 truncate text-body leading-5 font-semibold text-ink"
              >
                {project.repository}
              </h3>
              {view.pill !== null && (
                <span
                  className={`inline-flex h-5.5 shrink-0 items-center rounded-full px-2.5 font-mono text-micro font-semibold tracking-[1.2px] text-card ${chipFill[view.pill]}`}
                >
                  {view.pill}
                </span>
              )}
            </div>
            {control}
          </div>
          {readable !== null && phase !== null && (
            <div className="mt-[3.2px] flex items-baseline justify-between gap-4 text-label leading-4 font-semibold text-ink">
              <span className="break-words">{phase.name}</span>
              <span className="font-mono">{percent(phase)}%</span>
            </div>
          )}
          {phase !== null && (
            <div
              role="progressbar"
              aria-label={`${phase.name} in ${project.repository}`}
              aria-valuemin={0}
              aria-valuemax={100}
              aria-valuenow={percent(phase)}
              className="mt-1.5 h-1.5 overflow-hidden rounded-tag bg-track"
            >
              <div
                className="h-full rounded-tag bg-accent"
                style={{ width: `${percent(phase)}%` }}
              />
            </div>
          )}
          <div className="mt-1.5 text-cap leading-4 text-ink2">
            {view.lines.map((l, i) => (
              <p
                key={i}
                className={`${l.keep ? "" : "hidden md:block"} ${i === view.lines.length - 1 ? "md:text-muted" : ""}`}
              >
                {l.text}
              </p>
            ))}
          </div>
          {/* Details only while a phase runs, drains or is held: it opens the tickets in flight, and
              on the phone the status lines its card leaves out (owner, 2026-10-22). */}
          {(view.pill === "RUNNING" || view.pill === "STOPPING" || view.pill === "STOPPED") && (
            <button
              type="button"
              aria-expanded={open}
              aria-controls={detailId}
              aria-label={`${open ? "Hide details" : "Details"}: what is running in ${project.repository}`}
              className="-mb-3.5 -ml-1 -mt-1.5 flex w-fit min-h-11 min-w-11 cursor-pointer items-center px-1 text-label leading-4 text-accent"
              onClick={() => setOpen((v) => !v)}
            >
              {open ? "Hide details" : "Details"}
            </button>
          )}
        </div>
      </div>

      {project.snapshot?.unreadable && (
        <div className="mt-3 flex items-center justify-between gap-4">
          <p role="status" className="text-cap text-ink2">
            The phases could not be read at {new Date(project.snapshot.at).toLocaleTimeString()}.
          </p>
          <TryAgainPill onClick={refresh} />
        </div>
      )}
      {view.notice !== null && (
        <p id={noticeId} className="mt-0.5 text-cap leading-4 text-muted">
          {view.notice}
        </p>
      )}
      {resumeError && (
        <p role="alert" className="mt-3 text-cap text-stop">
          The resume was not made. Nothing changed; try again.
        </p>
      )}
      {moved !== null && (
        <p role="alert" className="mt-3 text-cap text-defer">
          The start was not requested: {moved}.
        </p>
      )}

      {readable !== null && open && (
        <div id={detailId} className="mt-4 border-t border-hair pt-3">
          <div className="mb-3 text-cap text-ink2 md:hidden">
            {view.lines
              .filter((l) => !l.keep)
              .map((l, i) => (
                <p key={i}>{l.text}</p>
              ))}
          </div>
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

      {/* Both dialogs sit in the body, so the card moving between columns never takes an open
          modal with it. */}
      {createPortal(
        <dialog
          ref={stopRef}
          aria-labelledby={`${detailId}-stop-title`}
          className="m-auto max-h-dvh w-full max-w-[min(var(--container-phone),calc(100dvw-2rem))] overflow-auto rounded-card border border-hair bg-card p-6 text-ink backdrop:bg-ink/40"
        >
          <h2 id={`${detailId}-stop-title`} className="text-head font-semibold">
            Stop {readable?.current ?? "the phase"} in {project.repository}?
          </h2>
          <p className="mt-3 text-body text-ink2">
            No new tickets start. {runsSentence(project.queue.in_flight)}{" "}
            {heldSentence(project.queue.held)}
          </p>
          {stopError && (
            <p role="alert" className="mt-3 text-body text-stop">
              The stop was not made. Nothing changed; try again.
            </p>
          )}
          <ButtonPair
            back={
              <ActionButton
                kind="secondary"
                label="BACK"
                onClick={() => stopRef.current?.close()}
              />
            }
            action={
              <ActionButton
                kind="primary"
                label="STOP PHASE"
                disabled={saving}
                onClick={() => pause("stop")}
              />
            }
          />
        </dialog>,
        document.body,
      )}

      {readable !== null &&
        createPortal(
          <dialog
            ref={dialogRef}
            aria-labelledby={`${detailId}-title`}
            className="m-auto max-h-dvh w-full max-w-[min(var(--container-phone),calc(100dvw-2rem))] overflow-auto rounded-card border border-hair bg-card p-6 text-ink backdrop:bg-ink/40"
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
            <ButtonPair
              back={
                <ActionButton
                  kind="secondary"
                  label="BACK"
                  onClick={() => dialogRef.current?.close()}
                />
              }
              action={
                <ActionButton kind="primary" label="START" disabled={saving} onClick={start} />
              }
            />
          </dialog>,
          document.body,
        )}
    </li>
  );
}

type CardProps = {
  projects: Project[];
  total: number;
  now: number;
  stale: boolean;
  // Offers the full list, which the page of every project does not.
  seeAll: boolean;
  refresh: () => void;
};

// ProjectsCard is ONE elevated card: each project a section split from the next by a rule, then
// the footer row with SEE ALL at the left and the ADD pill at the right. With no project listed
// the card is the footer alone.
function ProjectsCard({ projects, total, now, stale, seeAll, refresh }: CardProps) {
  const [adding, setAdding] = useState(false);
  const addRef = useRef<HTMLButtonElement>(null);
  return (
    <>
      {stale && (
        <p role="status" className="mt-4 text-cap text-defer">
          The list could not be refreshed; showing the last read.
        </p>
      )}
      <div className="card-frame mt-4">
        {projects.length > 0 && (
          <ul>
            {projects.map((project) => (
              <ProjectEntry
                key={project.repository}
                project={project}
                now={now}
                refresh={refresh}
              />
            ))}
          </ul>
        )}
        {/* The footer's rule and its 52px row, then 6px to the card's edge (less its 1px border). */}
        <div className={`pb-1.25 ${projects.length > 0 ? "border-t border-hair" : ""}`}>
          <div className="flex min-h-13 items-center justify-between gap-4">
            {projects.length === 0 ? (
              <p className="text-body">No project is listed.</p>
            ) : seeAll ? (
              <Link
                to="/projects"
                className="inline-flex min-h-13 min-w-11 items-center gap-[9.5px] text-label font-semibold text-ink"
              >
                See all {total} projects
                <svg
                  aria-hidden="true"
                  width="5"
                  height="10"
                  viewBox="0 0 5 10"
                  className="overflow-visible text-ink2"
                >
                  <path
                    d="M0 0l5 5l-5 5"
                    fill="none"
                    stroke="currentColor"
                    strokeWidth="1.75"
                    strokeLinecap="round"
                    strokeLinejoin="round"
                  />
                </svg>
              </Link>
            ) : (
              <span />
            )}
            <SmallPill
              kind="secondary"
              width={PROJECT_PILL_WIDTH}
              ref={addRef}
              label="ADD"
              aria-label="Add a project"
              onClick={() => setAdding(true)}
            />
          </div>
        </div>
      </div>
      <AddProjectDialog
        open={adding}
        onClose={() => {
          setAdding(false);
          addRef.current?.focus();
        }}
        onAdded={refresh}
      />
    </>
  );
}

type PanelsProps = {
  state: ProjectsState;
  retry: () => void;
  children: React.ReactNode;
};

// The loading and error panels both views share.
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
        <TryAgainPill onClick={retry} />
      </div>
    );
  }
  return children;
}

// ProjectsSection is the inbox's PROJECTS section: the newest three, then a
// link to the rest.
export function ProjectsSection() {
  const { state, refresh, retry } = useProjects();
  return (
    <section aria-labelledby="projects-heading" className="min-w-0">
      <div className="flex min-h-6.5 items-center">
        <h2 id="projects-heading" className="font-mono text-label font-semibold text-ink2">
          PROJECTS
        </h2>
      </div>
      <ProjectsPanels state={state} retry={retry}>
        <ProjectsCard
          projects={state.projects.slice(0, SECTION_PROJECTS)}
          total={state.total}
          now={state.readAt}
          stale={state.stale}
          seeAll={state.total > SECTION_PROJECTS}
          refresh={refresh}
        />
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
        <ProjectsCard
          projects={state.projects}
          total={state.total}
          now={state.readAt}
          stale={state.stale}
          seeAll={false}
          refresh={refresh}
        />
      </ProjectsPanels>
    </section>
  );
}
