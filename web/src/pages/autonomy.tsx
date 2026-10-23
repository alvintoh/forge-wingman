import { useEffect, useState } from "react";

// MergeState is the auto-merge state the endpoint serves: the owner's switch
// beside the rate's verdict over the trailing window, the bar it is compared to
// and the reason the pair gives.
type MergeState = {
  switch_on: boolean;
  rate_allows: boolean;
  rate_value: string;
  limit: string;
  // Window is how many trailing merged PRs the rate covers.
  window: number;
  effective: boolean;
  // Reason is why auto-merge is or is not effective: one of the endpoint's own
  // stable tokens, which reasonText words.
  reason: string;
};

// AutonomyView is what the autonomy page reads from the endpoint: the
// auto-merge state over the measured merges.
type AutonomyView = {
  auto_merge: MergeState;
};

type AutonomyState = {
  status: "loading" | "error" | "ready";
  view: AutonomyView;
};

// noView is what the section holds before the state is read and after a read
// that failed; it renders in neither state.
const noView: AutonomyView = {
  auto_merge: {
    switch_on: false,
    rate_allows: false,
    rate_value: "",
    limit: "",
    window: 0,
    effective: false,
    reason: "",
  },
};

// Autonomy shows whether an eligible pull request merges itself: the state over
// the trailing window, the rate behind it and the reason. The switch is the
// owner's setting, set with `runner auto-merge`.
export function Autonomy() {
  const [state, setState] = useState<AutonomyState>({ status: "loading", view: noView });

  useEffect(() => {
    const controller = new AbortController();

    async function read() {
      const response = await fetch("/api/autonomy", { signal: controller.signal });
      if (!response.ok) {
        throw new Error(`the autonomy endpoint answered ${response.status}`);
      }
      const body: unknown = await response.json();
      if (!isAutonomy(body)) {
        throw new Error("the autonomy endpoint answered no auto-merge state");
      }
      if (controller.signal.aborted) {
        return;
      }
      setState({ status: "ready", view: body });
    }

    read().catch((err: unknown) => {
      if (controller.signal.aborted) {
        return;
      }
      console.error("autonomyNotRead", err);
      setState({ status: "error", view: noView });
    });

    return () => controller.abort();
  }, []);

  const merge = state.view.auto_merge;

  return (
    <section className="mx-auto max-w-desk px-padx pt-4 pb-band">
      <h1 className="text-title font-semibold text-ink">Autonomy</h1>
      <p className="mt-2 text-body text-ink2">Whether an eligible PR merges itself.</p>
      {state.status === "loading" && (
        <p role="status" className="mt-6">
          Reading the auto-merge state…
        </p>
      )}
      {state.status === "error" && (
        <p role="alert" className="mt-6">
          The auto-merge state could not be read.
        </p>
      )}
      {state.status === "ready" && (
        <>
          <p className="mt-6 text-title font-semibold text-ink">
            {merge.effective ? "Auto-merge is on" : "Auto-merge is off"}
          </p>
          <p className="mt-1 text-body text-ink2">
            {`Rate over the trailing ${merge.window} merged PRs: ${merge.rate_value} (bar ${merge.limit}).`}
          </p>
          <p className="mt-2 text-body text-ink2">{reasonText(merge.reason)}</p>
        </>
      )}
    </section>
  );
}

// isAutonomy narrows the endpoint's answer to the one thing this view can check
// about a document it did not build: that it carries an auto-merge state.
function isAutonomy(body: unknown): body is AutonomyView {
  return (
    typeof body === "object" &&
    body !== null &&
    "auto_merge" in body &&
    typeof body.auto_merge === "object" &&
    body.auto_merge !== null
  );
}

// reasonText is why auto-merge is on or off, as the view words the endpoint's
// own reason tokens.
function reasonText(reason: string): string {
  switch (reason) {
    case "on":
      return "The rate allows it and the switch is on.";
    case "switch-off":
      return "The owner's switch is off.";
    case "switch-unread":
      return "The switch could not be read, so auto-merge is off.";
    case "too-few-measured":
      return "Fewer merged PRs than the window holds are measured, or one carries an unmeasured commit.";
    case "no-agent-lines":
      return "The measured PRs carry no agent lines, so there is no rate.";
    case "rate-above-bar":
      return "The trailing rate is above the bar.";
    case "reverted":
      return "An auto-merged PR of the window was reverted.";
    case "evidence-unread":
      return "The measured merges could not be read.";
    default:
      return reason;
  }
}
