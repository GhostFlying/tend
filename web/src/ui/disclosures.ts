import { useEffect } from "react";

/** A disclosure changes existing content, not the reader's viewport position. */
export function useStableDisclosures(): void {
  useEffect(() => {
    const activate = (event: MouseEvent) => {
      const control =
        event.target instanceof Element
          ? event.target.closest<HTMLElement>("summary, button[aria-expanded]")
          : null;
      if (!control) return;
      const top = control.getBoundingClientRect().top;
      const scrolls: HTMLElement[] = [];
      for (let node = control.parentElement; node; node = node.parentElement) {
        if (/auto|scroll/.test(getComputedStyle(node).overflowY)) scrolls.push(node);
      }
      const restore = () => {
        if (!control.isConnected) return;
        for (const node of scrolls) {
          node.scrollTop += control.getBoundingClientRect().top - top;
        }
      };
      // React commits and native details layout both settle before the next paint.
      requestAnimationFrame(() => {
        restore();
        requestAnimationFrame(restore);
      });
    };
    document.addEventListener("click", activate, true);
    return () => document.removeEventListener("click", activate, true);
  }, []);
}
