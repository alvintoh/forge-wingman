import { useId } from "react";
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
  // Classes that hide the PROJECTS caption, and take the card's top margin with it, at a width.
  headingClassName?: string;
  cardClassName?: string;
};

/**
 * ProjectFilterColumn is the PROJECTS caption over a radio list that narrows a page to one
 * project: All projects first, then one row per project. A chosen project is listed alone, under
 * the unchosen All row, which is the way back.
 */
export function ProjectFilterColumn({
  state,
  retry,
  known,
  onChoose,
  className,
  headingClassName,
  cardClassName = "",
}: ProjectFilterColumnProps) {
  const headingId = useId();
  const listed = known === undefined ? state.projects : [known];
  return (
    <section aria-labelledby={headingId} className={className}>
      <ProjectsHeading id={headingId} className={headingClassName} />
      <ProjectsPanels state={state} retry={retry}>
        {/* The card ends 6px under its last row, less its 1px edge. */}
        <div className={`card-frame mt-4 pb-1.25 max-sm:-mx-2.5 ${cardClassName}`}>
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
        </div>
      </ProjectsPanels>
    </section>
  );
}
