import { useId, useRef, useEffect, useContext, type ReactNode } from "react";
import { OverlayVisibility, useOverlayState } from "./focus.ts";
import { Button, type ButtonProps } from "./Button.tsx";
import { choiceIndex } from "./Choices.tsx";
import styles from "./Menu.module.css";

type MenuItem = { id: string; label: ReactNode; disabled?: boolean; onSelect(): void };
export function Menu({
  label,
  items,
  children,
  ...props
}: Omit<ButtonProps, "children" | "onClick"> & {
  label: string;
  items: readonly MenuItem[];
  children?: ReactNode;
}) {
  const id = useId();
  const anchor = `--menu-${id.replace(/[^a-zA-Z0-9-]/g, "")}`;
  const trigger = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const entries = useRef<(HTMLButtonElement | null)[]>([]);
  const [expanded, setExpanded] = useOverlayState(false);
  const visible = useContext(OverlayVisibility);
  useEffect(() => {
    if (!visible) menu.current?.hidePopover();
  }, [visible]);
  const close = () => {
    menu.current?.hidePopover();
    setExpanded(false);
    trigger.current?.focus();
  };
  const open = (last = false) => {
    menu.current?.showPopover();
    setExpanded(true);
    const index = choiceIndex(
      last ? "End" : "Home",
      0,
      items.map((item) => !!item.disabled),
    );
    if (index !== undefined) entries.current[index]?.focus();
    else menu.current?.focus();
  };
  return (
    <>
      <Button
        {...props}
        ref={trigger}
        aria-label={label}
        aria-haspopup="menu"
        aria-expanded={expanded}
        aria-controls={id}
        onClick={(event) => {
          event.preventDefault();
          if (menu.current?.matches(":popover-open")) close();
          else open();
        }}
        style={{ ...props.style, anchorName: anchor }}
        onKeyDown={(event) => {
          props.onKeyDown?.(event);
          if (event.defaultPrevented || (event.key !== "ArrowDown" && event.key !== "ArrowUp"))
            return;
          event.preventDefault();
          open(event.key === "ArrowUp");
        }}
      >
        {children ?? label}
      </Button>
      <div
        ref={menu}
        id={id}
        className={styles.menu}
        style={{ positionAnchor: anchor }}
        role="menu"
        aria-label={label}
        popover="auto"
        onToggle={(event) => {
          setExpanded(event.newState === "open");
        }}
        tabIndex={-1}
        onKeyDown={(event) => {
          if (event.key === "Escape") {
            event.preventDefault();
            event.stopPropagation();
            close();
            return;
          }
          if (event.key === "Tab") {
            menu.current?.hidePopover();
            return;
          }
          const current = entries.current.findIndex((entry) => entry === document.activeElement);
          const next = choiceIndex(
            event.key,
            current,
            items.map((item) => !!item.disabled),
          );
          if (next === undefined) return;
          event.preventDefault();
          entries.current[next]?.focus();
        }}
      >
        {items.map((item, index) => (
          <button
            key={item.id}
            type="button"
            ref={(entry) => {
              entries.current[index] = entry;
            }}
            role="menuitem"
            tabIndex={-1}
            disabled={item.disabled}
            onClick={() => {
              close();
              item.onSelect();
            }}
          >
            {item.label}
          </button>
        ))}
      </div>
    </>
  );
}
