import { Link } from "@tanstack/react-router";
import { captionClass } from "../caption";
import { StatusMark } from "../status-mark";
import { rateLine } from "./format";
import type { MergeState } from "./types";

const ON_WORD = "Auto-merge is on";
const OFF_WORD = "Auto-merge is off";

// Status is auto-merge's effectiveness as a shape and a word; the rate does not enter it.
function Status({ merge }: { merge: MergeState }) {
  const on = merge.effective;
  return (
    <>
      <StatusMark kind={on ? "on" : "off"} />
      <p
        role="status"
        className={`text-title leading-6 font-semibold ${on ? "text-ok" : "text-ink"}`}
      >
        {on ? ON_WORD : OFF_WORD}
      </p>
    </>
  );
}

// Rules says who merges what.
function Rules() {
  return (
    <ul className="mt-3 list-disc space-y-1.5 pl-3.5 text-label leading-4.5 text-ink2 marker:text-ink">
      <li>Merges itself: size S and M PRs while auto-merge is on.</li>
      <li>You merge: front-end changes, size L and sampled PRs.</li>
    </ul>
  );
}

// The outlined pill that links to the settings: 36x22 to the eye, 44x44 to the finger.
function SettingsPill() {
  return (
    <Link
      to="/config"
      aria-label="Auto-merge settings"
      className="group inline-flex min-h-11 min-w-11 shrink-0 items-center justify-center focus-visible:outline-none!"
    >
      <span className="inline-flex h-5.5 w-9 items-center justify-center rounded-full border border-ink2 bg-card text-ink2 group-focus-visible:outline-3 group-focus-visible:outline-offset-2 group-focus-visible:outline-accent">
        <svg aria-hidden="true" width="17" height="4" viewBox="0 0 21 5" fill="currentColor">
          <circle cx="2.5" cy="2.5" r="2.5" />
          <circle cx="10.5" cy="2.5" r="2.5" />
          <circle cx="18.5" cy="2.5" r="2.5" />
        </svg>
      </span>
    </Link>
  );
}

type MergeStatusCardProps = {
  merge: MergeState;
  // Whether a project is chosen: the status stays pooled, and the card says so.
  projectChosen: boolean;
};

/**
 * MergeStatusCard is the Autonomy page's auto-merge card: the caption and the settings pill, the
 * state as a shape and a word, the rate beside its guide, and who merges what. The page draws it
 * once the state has been read; the wait and the failure are page text, not this card.
 */
export function MergeStatusCard({ merge, projectChosen }: MergeStatusCardProps) {
  return (
    <section aria-labelledby="auto-merge-card" className="card-frame py-4 max-sm:-mx-2.5">
      <h2 id="auto-merge-card" className={`${captionClass} text-ink2`}>
        auto-merge
      </h2>
      <div className="mt-1 flex min-h-11 items-center justify-between gap-3">
        <div className="flex items-center gap-2">
          <Status merge={merge} />
        </div>
        <SettingsPill />
      </div>
      <p className="mt-1 text-body leading-5.5 text-ink2">{rateLine(merge)}</p>
      <Rules />
      {projectChosen && (
        <p className="mt-2 text-label font-semibold text-ink">rate: all projects</p>
      )}
    </section>
  );
}
