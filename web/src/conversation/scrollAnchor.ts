// Anchor reading to visible messages, independent of content added below them.
export interface ScrollAnchor {
  items: { id: string; offset: number }[];
}

const selector = "[data-scroll-id]";

export function captureAnchor(root: HTMLElement): ScrollAnchor {
  const top = root.getBoundingClientRect().top;
  const bottom = top + root.clientHeight;
  const items: ScrollAnchor["items"] = [];
  const nodes = [...root.querySelectorAll<HTMLElement>(selector)];
  // Prefer a row beginning in view over a tall earlier row spanning the viewport.
  // Growth within that earlier row must not displace the later row being read.
  nodes.sort(
    (a, b) =>
      Number(a.getBoundingClientRect().top < top) - Number(b.getBoundingClientRect().top < top),
  );
  for (const item of nodes) {
    const bounds = item.getBoundingClientRect();
    if (bounds.bottom > top && bounds.top < bottom) {
      items.push({ id: item.dataset.scrollId!, offset: bounds.top - top });
      // Nearby fallbacks survive a local outbox row becoming a ledger item.
      if (items.length === 3) break;
    }
  }
  return { items };
}

export function restoreAnchor(root: HTMLElement, anchor: ScrollAnchor): void {
  const nodes = [...root.querySelectorAll<HTMLElement>(selector)];
  const top = root.getBoundingClientRect().top;
  for (const saved of anchor.items) {
    const item = nodes.find((node) => node.dataset.scrollId === saved.id);
    if (item) {
      root.scrollTop += item.getBoundingClientRect().top - top - saved.offset;
      return;
    }
  }
}
