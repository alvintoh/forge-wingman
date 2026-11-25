/** The step a radio group's arrow key takes: forward for down and right, back for up and left, else none. */
export function arrowStep(key: string): 1 | -1 | 0 {
  if (key === "ArrowDown" || key === "ArrowRight") {
    return 1;
  }
  if (key === "ArrowUp" || key === "ArrowLeft") {
    return -1;
  }
  return 0;
}

/** wrapIndex is index counted round a list of count, so one step past either end lands on the other. */
export function wrapIndex(index: number, count: number): number {
  return ((index % count) + count) % count;
}

/**
 * moveCheck is the key handler of a radio group that opts in to the arrow keys: they move the check
 * to the next or previous radio of the group and focus with it, as the WAI-ARIA radio group pattern
 * has it. The radio moved to is clicked, so its own handler checks it.
 */
export function moveCheck(event: React.KeyboardEvent<HTMLButtonElement>) {
  const step = arrowStep(event.key);
  if (step === 0) {
    return;
  }
  event.preventDefault();
  const group = event.currentTarget.closest('[role="radiogroup"]');
  const radios = [...(group?.querySelectorAll<HTMLButtonElement>('[role="radio"]') ?? [])];
  const next = radios[wrapIndex(radios.indexOf(event.currentTarget) + step, radios.length)];
  next?.focus();
  next?.click();
}
