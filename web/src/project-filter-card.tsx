import { useId, useState } from "react";
import { DisclosureRow } from "./disclosure-row";
import { AllProjectsRow, RadioChoice } from "./filter-radio";
import { displayName, type Project, type ProjectsState } from "./project-list";
import { NO_PROJECT_LISTED, ProjectStateChip, ProjectsHeading, ProjectsPanels } from "./projects";

type RowProps = {
  project: Project;
  // How the row names the project: its tooltip and its radio's name read the same.
  name: string;
  checked: boolean;
  onChoose: (repository: string) => void;
};

function ProjectRow({ project, name, checked, onChoose }: RowProps) {
  const nameId = useId();
  return (
    <li
      role="none"
      className="relative flex min-h-13 items-center gap-2.5 border-t border-hair pl-8"
    >
      <RadioChoice
        labelledBy={nameId}
        checked={checked}
        onChoose={() => onChoose(project.repository)}
        bulletClass="top-1/2 -translate-y-1/2"
      >
        {/* The name's tooltip, in the radio over it: on the radio it would be read twice. */}
        <span
          aria-hidden="true"
          title={name}
          className="absolute inset-y-0 right-padx left-[calc(var(--spacing-padx)+2rem)]"
        />
      </RadioChoice>
      <span id={nameId} className="min-w-0 truncate text-body font-semibold text-ink">
        {name}
      </span>
      <ProjectStateChip project={project} />
    </li>
  );
}

type ProjectFilterColumnProps = {
  state: ProjectsState;
  retry: () => void;
  // The listed project the filter is on, or undefined for all of them.
  known: Project | undefined;
  onChoose: (repository: string | null) => void;
  className?: string;
  // Whether the wide layout draws no PROJECTS caption, so the column starts with its first card or sentence.
  captionless?: boolean;
};

const FILTER_PROJECTS = 3;
const WIDE_HIDDEN = "@[51.25rem]:hidden";
const GAP_UNDER_CAPTION = "mt-4";
const WIDE_NO_GAP = "@[51.25rem]:mt-0";

/**
 * ProjectFilterColumn is the PROJECTS caption over a radio list that narrows a page to one
 * project: All projects first, then the first three projects, with a link that opens the rest in
 * place. A chosen project is listed alone, under the unchosen All row, which is the way back.
 */
export function ProjectFilterColumn({
  state,
  retry,
  known,
  onChoose,
  className,
  captionless = false,
}: ProjectFilterColumnProps) {
  const headingId = useId();
  const gap = captionless ? `${GAP_UNDER_CAPTION} ${WIDE_NO_GAP}` : GAP_UNDER_CAPTION;
  const [open, setOpen] = useState(false);
  const folds = known === undefined && state.projects.length > FILTER_PROJECTS;
  if (!folds && open) {
    setOpen(false);
  }
  const all = known === undefined ? state.projects : [known];
  const listed = folds && !open ? all.slice(0, FILTER_PROJECTS) : all;
  return (
    <section aria-labelledby={headingId} className={className}>
      <ProjectsHeading id={headingId} className={captionless ? WIDE_HIDDEN : undefined} />
      <ProjectsPanels state={state} retry={retry} className={gap}>
        {/* The card ends 6px under its last row, less its 1px edge. */}
        <div className={`card-frame ${gap} pb-1.25 max-sm:-mx-2.5`}>
          {listed.length === 0 ? (
            <p className="flex min-h-13 items-center text-body">{NO_PROJECT_LISTED}</p>
          ) : (
            <ul role="radiogroup" aria-label="Project filter">
              <li role="none">
                <AllProjectsRow checked={known === undefined} onChoose={() => onChoose(null)} />
              </li>
              {listed.map((project) => (
                <ProjectRow
                  key={project.repository}
                  project={project}
                  name={displayName(project.repository, state.projects)}
                  checked={known !== undefined}
                  onChoose={onChoose}
                />
              ))}
            </ul>
          )}
          {folds && (
            <DisclosureRow
              open={open}
              label={`Show all ${state.projects.length} projects`}
              onClick={() => setOpen((was) => !was)}
              className="pl-8"
            />
          )}
        </div>
      </ProjectsPanels>
    </section>
  );
}
