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
      const scrolls: { node: HTMLElement; previousTop: number | null }[] = [];
      for (let node = control.parentElement; node; node = node.parentElement) {
        if (/auto|scroll/.test(getComputedStyle(node).overflowY))
          scrolls.push({ node, previousTop: node.scrollTop });
      }
      const restore = () => {
        if (!control.isConnected) return;
        for (const scroll of scrolls) {
          const { node, previousTop } = scroll;
          if (previousTop === null) continue;
          // A collapse can clamp the old position. Any other movement takes
          // precedence over the disclosure's scheduled correction.
          const expectedTop = Math.min(
            previousTop,
            Math.max(0, node.scrollHeight - node.clientHeight),
          );
          if (Math.abs(node.scrollTop - expectedTop) >= 1) {
            scroll.previousTop = null;
            continue;
          }
          node.scrollTop += control.getBoundingClientRect().top - top;
          scroll.previousTop = node.scrollTop;
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
