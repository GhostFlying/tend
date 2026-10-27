import { useLayoutEffect, useState, type CSSProperties, type RefObject } from "react";

// Portaled composer panels follow their trigger without inheriting dock clipping.
export function useAnchoredPanel(
  open: boolean,
  trigger: RefObject<HTMLElement | null>,
  preferredWidth: number,
): CSSProperties {
  const [position, setPosition] = useState<CSSProperties>({ visibility: "hidden" });
  useLayoutEffect(() => {
    if (!open) return;
    const place = () => {
      const anchor = trigger.current;
      const rect = anchor?.getBoundingClientRect();
      if (
        !anchor ||
        !rect ||
        rect.width === 0 ||
        rect.height === 0 ||
        getComputedStyle(anchor).visibility !== "visible"
      ) {
        setPosition({ visibility: "hidden" });
        return;
      }
      const width = Math.min(innerWidth <= 760 ? innerWidth - 24 : preferredWidth, innerWidth - 24);
      const top = Math.max(20, Math.min(rect.top, innerHeight - 20));
      const bottom = Math.max(20, Math.min(rect.bottom, innerHeight - 20));
      const above = Math.max(0, top - 20);
      const below = Math.max(0, innerHeight - bottom - 20);
      const upwards = above >= below;
      setPosition({
        position: "fixed",
        left: Math.max(12, Math.min(rect.right - width, innerWidth - width - 12)),
        right: "auto",
        top: upwards ? "auto" : bottom + 8,
        bottom: upwards ? innerHeight - top + 8 : "auto",
        width,
        maxHeight: Math.min(360, upwards ? above : below),
        overflowY: "auto",
      });
    };
    place();
    const visibility = new MutationObserver(place);
    for (
      let ancestor: HTMLElement | null = trigger.current;
      ancestor;
      ancestor = ancestor.parentElement
    ) {
      visibility.observe(ancestor, {
        attributes: true,
        attributeFilter: ["hidden", "class", "style", "data-view"],
      });
    }
    window.addEventListener("resize", place);
    window.addEventListener("scroll", place, true);
    return () => {
      window.removeEventListener("resize", place);
      window.removeEventListener("scroll", place, true);
      visibility.disconnect();
    };
  }, [open, trigger, preferredWidth]);
  return position;
}
