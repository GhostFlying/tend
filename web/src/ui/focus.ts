const openers = new WeakMap<HTMLDialogElement, HTMLElement>();

export function rememberOpener(dialog: HTMLDialogElement, opener: Element | null) {
  if (opener instanceof HTMLElement && !dialog.contains(opener)) openers.set(dialog, opener);
}

export function navigationOpener(element: HTMLElement | null): HTMLElement | null {
  const dialog = element?.closest("dialog");
  const opener = dialog ? openers.get(dialog) : undefined;
  return opener ? navigationOpener(opener) : element;
}
