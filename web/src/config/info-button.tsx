import { useEffect, useId, useRef, useState } from "react";

type InfoButtonProps = {
  name: string;
  text: string;
};

/**
 * InfoButton is a 10px "i" set as a superscript 3px right of a row's value, in a 44x44 target that
 * ends on the card's edge, and the popover it discloses above it; its row is positioned and its
 * value sits at the row's right end. Click, tap or keyboard focus opens it; a second press, Escape,
 * leaving it or a press elsewhere closes it, and hover alone never opens it.
 */
export function InfoButton({ name, text }: InfoButtonProps) {
  const [open, setOpen] = useState(false);
  const popoverId = useId();
  const wrapRef = useRef<HTMLSpanElement>(null);
  const buttonRef = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    if (!open) {
      return;
    }
    function onKey(event: KeyboardEvent) {
      if (event.key === "Escape") {
        setOpen(false);
        buttonRef.current?.focus();
      }
    }
    function onPress(event: PointerEvent) {
      if (event.target instanceof Node && !wrapRef.current?.contains(event.target)) {
        setOpen(false);
      }
    }
    document.addEventListener("keydown", onKey);
    document.addEventListener("pointerdown", onPress);
    return () => {
      document.removeEventListener("keydown", onKey);
      document.removeEventListener("pointerdown", onPress);
    };
  }, [open]);

  // A pointer focuses the button on its way to the click, which toggles; only keyboard focus opens.
  function handleFocus(event: React.FocusEvent<HTMLButtonElement>) {
    if (event.currentTarget.matches(":focus-visible")) {
      setOpen(true);
    }
  }

  return (
    <span ref={wrapRef}>
      {/* The glyph's centre sits 8px right of the value and 1px above its capitals. */}
      <button
        ref={buttonRef}
        type="button"
        aria-label={name}
        aria-expanded={open}
        aria-controls={popoverId}
        onClick={() => setOpen((was) => !was)}
        onFocus={handleFocus}
        onBlur={() => setOpen(false)}
        className="group absolute -top-4.25 -right-padx size-11 cursor-pointer text-ink2 focus-visible:outline-none"
      >
        <svg
          aria-hidden="true"
          width="12"
          height="12"
          viewBox="0 0 12 12"
          fill="none"
          stroke="currentColor"
          strokeWidth="1.25"
          strokeLinecap="round"
          className="absolute top-4 left-5 rounded-full group-focus-visible:outline-2 group-focus-visible:outline-offset-1 group-focus-visible:outline-accent"
        >
          <circle cx="6" cy="6" r="5" />
          <path d="M6 5.75v2.5M6 3.75h.01" />
        </svg>
      </button>
      <span
        id={popoverId}
        hidden={!open}
        // A press on the text keeps the button's focus, so the popover stays open.
        onPointerDown={(event) => event.preventDefault()}
        className="absolute -top-1.5 -right-5 z-20 -translate-y-full text-left w-[min(17.5rem,100%+1.25rem)] rounded-control border border-hair bg-card p-4 text-label leading-4.5 font-normal text-ink shadow-[0_8px_28px_color-mix(in_srgb,var(--color-ink)_16%,transparent)]"
      >
        {text}
        {/* The caret, under the glyph's centre, 12px in from the popover's right edge. */}
        <svg
          aria-hidden="true"
          width="14"
          height="7"
          viewBox="0 0 14 7"
          className="absolute top-full right-[5px] -mt-px block"
        >
          <path d="M0 0.5L7 6.5L14 0.5" className="fill-card stroke-hair" />
        </svg>
      </span>
    </span>
  );
}
