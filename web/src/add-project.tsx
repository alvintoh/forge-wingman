import { useEffect, useId, useRef, useState } from "react";
import { ActionButton, ButtonPair } from "./action-button";
import { postJSON } from "./post-json";

// The slug of the runner's GitHub App, which names its install page.
const GITHUB_APP_SLUG = "forge-wingman-runner";
const INSTALL_URL = `https://github.com/apps/${GITHUB_APP_SLUG}/installations/new`;

// What the surface checks before it adds: owner/name, and GitHub's longest owner (39) and
// name (100) around the "/".
const REPOSITORY_PATTERN = /^[A-Za-z0-9._-]+\/[A-Za-z0-9._-]+$/;
const MAX_REPOSITORY = 140;

type Options = { status: "loading" } | { status: "error" } | { status: "ready"; names: string[] };

type Problem = { refusal: string | null };

function isOptions(body: unknown): body is { names: string[] } {
  return (
    typeof body === "object" &&
    body !== null &&
    "names" in body &&
    Array.isArray(body.names) &&
    body.names.every((name) => typeof name === "string")
  );
}

async function readOptions(signal: AbortSignal): Promise<string[]> {
  const response = await fetch("/api/projects/options", { signal });
  if (!response.ok) {
    throw new Error(`the options endpoint answered ${response.status}`);
  }
  const body: unknown = await response.json();
  if (!isOptions(body)) {
    throw new Error("the options endpoint answered no names");
  }
  return body.names;
}

const fieldClass =
  "block h-12 w-full rounded-tile border border-ink2 bg-ground pl-3.25 text-head text-ink placeholder:text-muted";

type DialogProps = {
  open: boolean;
  onClose: () => void;
  onAdded: () => void;
};

// AddProjectDialog picks a Linear project and takes the repository as typed. It opens when
// `open` turns true, reads the Linear projects not yet listed, and adds nothing on Escape or BACK.
export function AddProjectDialog({ open, onClose, onAdded }: DialogProps) {
  const dialogRef = useRef<HTMLDialogElement>(null);
  const addRef = useRef<HTMLButtonElement>(null);
  // attemptRef counts the adds and the closes, so a post's outcome is shown only while the dialog
  // it was made from is still the one open.
  const attemptRef = useRef(0);
  const titleId = useId();
  const helperId = useId();
  const [options, setOptions] = useState<Options>({ status: "loading" });
  const [linearProject, setLinearProject] = useState("");
  const [repository, setRepository] = useState("");
  const [saving, setSaving] = useState(false);
  const [problem, setProblem] = useState<Problem | null>(null);

  useEffect(() => {
    const dialog = dialogRef.current;
    if (dialog === null) {
      return;
    }
    if (!open) {
      if (dialog.open) {
        dialog.close();
      }
      return;
    }
    if (!dialog.open) {
      dialog.showModal();
    }
    const controller = new AbortController();
    readOptions(controller.signal).then(
      (names) => setOptions({ status: "ready", names }),
      (err: unknown) => {
        if (!controller.signal.aborted) {
          console.error("projectOptionsNotRead", err);
          setOptions({ status: "error" });
        }
      },
    );
    return () => controller.abort();
  }, [open]);

  // The button is disabled while the add is in flight, which drops focus; a failure returns it.
  useEffect(() => {
    if (problem !== null) {
      addRef.current?.focus();
    }
  }, [problem]);

  const typed = repository.trim();
  const repositoryValid = REPOSITORY_PATTERN.test(typed) && typed.length <= MAX_REPOSITORY;
  const valid = linearProject !== "" && repositoryValid;

  function handleClose() {
    attemptRef.current += 1;
    setOptions({ status: "loading" });
    setLinearProject("");
    setRepository("");
    setProblem(null);
    setSaving(false);
    onClose();
  }

  // add posts the pair; a refusal keeps the dialog open with its reason, and a failure says
  // nothing was added. A dialog closed while the post is in flight no longer shows its outcome,
  // so a late refusal cannot greet the next open; an add that went through still refreshes the list.
  async function add() {
    const attempt = ++attemptRef.current;
    const current = () => attempt === attemptRef.current;
    setSaving(true);
    setProblem(null);
    try {
      const response = await postJSON("/api/projects/add", {
        linear_project: linearProject,
        repository: typed,
      });
      if (!current()) {
        if (response.ok) {
          onAdded();
        }
        return;
      }
      if (response.status === 409) {
        const reason = (await response.text()).trim();
        if (current()) {
          setProblem({ refusal: reason });
        }
        return;
      }
      if (!response.ok) {
        throw new Error(`the add endpoint answered ${response.status}`);
      }
      onAdded();
      dialogRef.current?.close();
    } catch (err: unknown) {
      console.error("projectNotAdded", err);
      if (current()) {
        setProblem({ refusal: null });
      }
    } finally {
      if (current()) {
        setSaving(false);
      }
    }
  }

  const names = options.status === "ready" ? options.names : [];

  return (
    <dialog
      ref={dialogRef}
      aria-labelledby={titleId}
      onClose={handleClose}
      className="m-auto max-h-dvh w-full max-w-[min(28.75rem,calc(100dvw-2rem))] scroll-py-1.5 overflow-auto rounded-card border border-hair bg-card px-6.25 pt-7.25 pb-5.25 text-ink backdrop:bg-ink/40"
    >
      <form
        noValidate
        onSubmit={(event) => {
          event.preventDefault();
          if (valid && !saving) {
            void add();
          }
        }}
      >
        <h2 id={titleId} className="text-head leading-6 font-semibold">
          Add a project
        </h2>

        <label className="mt-4.5 block">
          <span className="block font-mono text-label leading-4 font-semibold tracking-[1.4px] text-ink2">
            LINEAR PROJECT
          </span>
          <span className="relative mt-1.5 block">
            <select
              value={linearProject}
              onChange={(event) => setLinearProject(event.target.value)}
              className={`${fieldClass} appearance-none pr-12 ${linearProject === "" ? "text-muted" : ""}`}
            >
              <option value="" disabled>
                {options.status === "loading" ? "Reading the projects…" : "Choose one"}
              </option>
              {names.map((name) => (
                <option key={name} value={name} className="text-ink">
                  {name}
                </option>
              ))}
            </select>
            <svg
              aria-hidden="true"
              width="10"
              height="5"
              viewBox="0 0 10 5"
              className="pointer-events-none absolute top-1/2 right-4 -translate-y-1/2 overflow-visible text-ink2"
            >
              <path
                d="M0 0l5 5l5-5"
                fill="none"
                stroke="currentColor"
                strokeWidth="1.75"
                strokeLinecap="round"
                strokeLinejoin="round"
              />
            </svg>
          </span>
        </label>
        {options.status === "error" && (
          <p role="alert" className="mt-2 text-cap leading-4 text-stop">
            The Linear projects could not be read. Close this and try again.
          </p>
        )}
        {options.status === "ready" && names.length === 0 && (
          <p role="status" className="mt-2 text-cap leading-4 text-muted">
            No Linear project is left to add.
          </p>
        )}

        <label className="mt-2.5 block">
          <span className="block font-mono text-label leading-4 font-semibold tracking-[1.4px] text-ink2">
            REPOSITORY
          </span>
          <input
            type="text"
            inputMode="url"
            value={repository}
            maxLength={MAX_REPOSITORY}
            placeholder="owner/name"
            autoCapitalize="none"
            autoComplete="off"
            autoCorrect="off"
            spellCheck={false}
            aria-describedby={helperId}
            aria-invalid={repository !== "" && !repositoryValid}
            onChange={(event) => setRepository(event.target.value)}
            className={`${fieldClass} mt-1.5 pr-3.25 ${repository === "" ? "" : "font-mono"}`}
          />
        </label>
        <p id={helperId} className="mt-2 text-cap leading-4 text-muted">
          Type owner/name. The runner&apos;s GitHub App must be installed on it first.
        </p>
        <a
          href={INSTALL_URL}
          target="_blank"
          rel="noopener noreferrer"
          className="relative flex min-h-11 w-fit items-center text-label font-semibold text-accent underline"
        >
          Install the App on GitHub<span className="sr-only"> (opens in a new tab)</span>
        </a>
        <p className="-mt-[1.75px] text-cap leading-4 text-muted">
          The dispatcher checks it can reach the repository on its next poll.
        </p>

        {problem !== null && (
          <p
            role="alert"
            className={`mt-3 text-body ${problem.refusal === null ? "text-stop" : "text-defer"}`}
          >
            {problem.refusal === null
              ? "The add was not made. Nothing was added."
              : `The add was not made: ${problem.refusal}.`}
          </p>
        )}
        <ButtonPair
          margin="mt-4.5"
          back={
            <ActionButton
              kind="secondary"
              label="BACK"
              onClick={() => dialogRef.current?.close()}
            />
          }
          action={
            <ActionButton
              ref={addRef}
              kind="primary"
              type="submit"
              label="ADD PROJECT"
              disabled={!valid || saving}
            />
          }
        />
      </form>
    </dialog>
  );
}
