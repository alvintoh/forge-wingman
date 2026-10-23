import { RewriteSection } from "../autonomy/rewrite-section";
import { isAutonomy } from "../autonomy/guard";
import { useRead } from "../stats/use-read";

// Autonomy shows whether an eligible pull request merges itself: the state over
// the trailing window, the rate behind it and the reason, then the rewrite rate
// as charts. The switch is the owner's setting, set with `runner auto-merge`.
export function Autonomy() {
  const read = useRead("/api/autonomy", isAutonomy, "autonomyNotRead");

  return (
    <section className="mx-auto max-w-desk px-padx pt-4 pb-band">
      <h1 className="text-title font-semibold text-ink">Autonomy</h1>
      <p className="mt-2 text-body text-ink2">Whether an eligible PR merges itself.</p>
      {read.status === "loading" && (
        <p role="status" className="mt-6">
          Reading the auto-merge state…
        </p>
      )}
      {read.status === "error" && (
        <p role="alert" className="mt-6">
          The auto-merge state could not be read.
        </p>
      )}
      {read.status === "ready" && (
        <>
          <p className="mt-6 text-title font-semibold text-ink">
            {read.body.auto_merge.effective ? "Auto-merge is on" : "Auto-merge is off"}
          </p>
          <p className="mt-1 text-body text-ink2">
            {`Rate over the trailing ${read.body.auto_merge.window} merged PRs: ${read.body.auto_merge.rate_value} (bar ${read.body.auto_merge.limit}).`}
          </p>
          <p className="mt-2 text-body text-ink2">{reasonText(read.body.auto_merge.reason)}</p>
          <RewriteSection rewrite={read.body.rewrite} />
        </>
      )}
    </section>
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
