import { createContext, useContext, useState } from "react";

export const OverlayVisibility = createContext(true);

/** Reset only transient presentation state when a retained conversation is hidden. */
export function useOverlayState<T>(initial: T, visible?: boolean) {
  const inherited = useContext(OverlayVisibility);
  const active = visible ?? inherited;
  const [value, setValue] = useState(initial);
  if (!active && !Object.is(value, initial)) setValue(initial);
  return [active ? value : initial, setValue] as const;
}

const openers = new WeakMap<HTMLDialogElement, HTMLElement>();

export function rememberOpener(dialog: HTMLDialogElement, opener: Element | null) {
  if (opener instanceof HTMLElement && !dialog.contains(opener)) openers.set(dialog, opener);
}

export function navigationOpener(element: HTMLElement | null): HTMLElement | null {
  const dialog = element?.closest("dialog");
  const opener = dialog ? openers.get(dialog) : undefined;
  return opener ? navigationOpener(opener) : element;
}
