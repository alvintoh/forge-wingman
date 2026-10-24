import { barOf, reasonText, trailingWords, whole } from "../autonomy/format";
import { isAutonomy } from "../autonomy/guard";
import type { MergeState } from "../autonomy/types";
import { captionClass } from "./caption";
import { PhaseModels } from "./phase-models";
import { useRead } from "./use-read";

type Status = "on" | "off" | "held";

const STATUS_WORD: Record<Status, string> = {
  on: "Auto-merge is on",
  off: "Auto-merge is off",
  held: "Auto-merge is held",
};

// on is the switch and the rate agreeing; off is the owner's switch; held is the switch on while
// the rate does not allow it.
function statusOf(merge: MergeState): Status {
  if (merge.effective) {
    return "on";
  }
  return merge.switch_on ? "held" : "off";
}

// The status as a shape beside its word: a tick in a disc, a bar in a ring, a pause mark in a ring.
function StatusMark({ status }: { status: Status }) {
  return (
    <svg aria-hidden="true" width="18" height="18" viewBox="0 0 18 18" className="shrink-0">
      {status === "on" ? (
        <>
          <circle cx="9" cy="9" r="9" className="fill-ok" />
          <path
            d="M3.5 9.5l4 4l7-8"
            fill="none"
            strokeWidth="2"
            strokeLinecap="round"
            strokeLinejoin="round"
            className="stroke-card"
          />
        </>
      ) : (
        <>
          <circle cx="9" cy="9" r="8" fill="none" strokeWidth="2" className="stroke-ink2" />
          {status === "off" ? (
            <rect x="4" y="8" width="10" height="2" className="fill-ink2" />
          ) : (
            <>
              <rect x="5.5" y="4" width="2.5" height="10" className="fill-ink2" />
              <rect x="10" y="4" width="2.5" height="10" className="fill-ink2" />
            </>
          )}
        </>
      )}
    </svg>
  );
}

// Summary is the state in words: the status as a shape and a word, the rate line, and who merges what.
function Summary({ merge }: { merge: MergeState }) {
  const status = statusOf(merge);
  return (
    <>
      <div className="mt-1 flex items-center gap-2">
        <StatusMark status={status} />
        <p
          role="status"
          className={`text-title leading-6 font-semibold ${status === "on" ? "text-ok" : "text-ink"}`}
        >
          {STATUS_WORD[status]}
        </p>
      </div>
      <p className="mt-1.5 text-label leading-4.5 text-ink2">
        {merge.rate_allows ? `can be on: ${trailingWords(merge)}` : reasonText(merge.reason)}
      </p>
      <ul className="mt-3 list-disc space-y-1.5 pl-3.5 text-label leading-4.5 text-ink2 marker:text-ink">
        <li>
          {`Merges itself: size S and M PRs while the trailing ${merge.window} is ${whole(barOf(merge.limit))} or less.`}
        </li>
        <li>You merge: front-end changes, size L and sampled PRs.</li>
      </ul>
    </>
  );
}

// The outlined pill that links to the settings: 44x28 to the eye, 44x44 to the finger.
function SettingsPill() {
  return (
    <a
      href="/config"
      aria-label="Auto-merge settings"
      className="group inline-flex min-h-11 min-w-11 shrink-0 items-center justify-center focus-visible:outline-none!"
    >
      <span className="inline-flex h-7 w-11 items-center justify-center rounded-full border border-ink2 bg-card text-ink2 group-focus-visible:outline-3 group-focus-visible:outline-offset-2 group-focus-visible:outline-accent">
        <svg aria-hidden="true" width="21" height="5" viewBox="0 0 21 5" fill="currentColor">
          <circle cx="2.5" cy="2.5" r="2.5" />
          <circle cx="10.5" cy="2.5" r="2.5" />
          <circle cx="18.5" cy="2.5" r="2.5" />
        </svg>
      </span>
    </a>
  );
}

/**
 * AutoMerge is the Statistics card for auto-merge: its state as a shape and a word, the rate line,
 * who merges what, and each phase's models. The pill links to the settings page.
 */
export function AutoMerge() {
  const read = useRead("/api/autonomy", isAutonomy, "autonomyNotRead");
  const view = read.status === "ready" ? read.body : null;

  return (
    <section aria-labelledby="auto-merge-title" className="card-frame py-4">
      <div className="flex min-h-11 items-center justify-between gap-3">
        <h2 id="auto-merge-title" className={`${captionClass} text-ink2`}>
          auto-merge
        </h2>
        {view !== null && <SettingsPill />}
      </div>
      {read.status === "loading" && (
        <p role="status" className="text-label text-ink2">
          Reading the auto-merge state…
        </p>
      )}
      {read.status === "error" && (
        <p role="alert" className="text-label text-ink2">
          The auto-merge state could not be read.
        </p>
      )}
      {view !== null && (
        <>
          <Summary merge={view.auto_merge} />
          <PhaseModels figures={view.rewrite} bar={barOf(view.auto_merge.limit)} />
        </>
      )}
    </section>
  );
}
