/**
 * showModalOnBack opens a confirm with BACK focused. Under 600px the action comes first in the
 * document, so the browser would pick it.
 */
export function showModalOnBack(dialog: HTMLDialogElement | null, back: HTMLElement | null) {
  dialog?.showModal();
  back?.focus();
}
