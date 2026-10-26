/**
 * settingsPillClass is the outlined three-dot pill both entries to the settings draw: 28px in a
 * 44px hit area, whose visible edge ends on the card's text edge.
 */
export const settingsPillClass =
  "inline-flex h-7 w-11 items-center justify-center rounded-full border border-ink2 bg-card text-ink2 group-focus-visible:outline-3 group-focus-visible:outline-offset-2 group-focus-visible:outline-accent";

/** SettingsDots is the pill's three-dot glyph. */
export function SettingsDots() {
  return (
    <svg aria-hidden="true" width="21" height="5" viewBox="0 0 21 5" fill="currentColor">
      <circle cx="2.5" cy="2.5" r="2.5" />
      <circle cx="10.5" cy="2.5" r="2.5" />
      <circle cx="18.5" cy="2.5" r="2.5" />
    </svg>
  );
}

type SettingsButtonProps = {
  onClick: () => void;
  buttonRef?: React.Ref<HTMLButtonElement>;
};

/**
 * SettingsButton opens the Config settings modal, as the same outlined pill the statistics page
 * links with.
 */
export function SettingsButton({ onClick, buttonRef }: SettingsButtonProps) {
  return (
    <button
      ref={buttonRef}
      type="button"
      aria-label="Settings"
      aria-haspopup="dialog"
      onClick={onClick}
      className="group inline-flex min-h-11 min-w-11 shrink-0 items-center justify-center focus-visible:outline-none!"
    >
      <span className={settingsPillClass}>
        <SettingsDots />
      </span>
    </button>
  );
}
