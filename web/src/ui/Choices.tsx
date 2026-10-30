import { useId, useRef, type ReactNode } from "react";
import styles from "./Choices.module.css";

export type ChoiceItem<T extends string> = { value: T; label: ReactNode; disabled?: boolean };

/** Resolve keyboard navigation without visiting disabled choices. */
export function choiceIndex(
  key: string,
  current: number,
  disabled: readonly boolean[],
): number | undefined {
  const enabled = disabled.flatMap((off, index) => (off ? [] : [index]));
  if (!enabled.length) return undefined;
  if (key === "Home") return enabled[0];
  if (key === "End") return enabled.at(-1);
  const direction =
    key === "ArrowRight" || key === "ArrowDown"
      ? 1
      : key === "ArrowLeft" || key === "ArrowUp"
        ? -1
        : 0;
  if (!direction) return undefined;
  const index = enabled.indexOf(current);
  return enabled[(index + direction + enabled.length) % enabled.length];
}

type ChoiceProps<T extends string> = {
  label: string;
  items: readonly ChoiceItem<T>[];
  value: T;
  onChange(value: T): void;
  className?: string;
};

export function SegmentedControl<T extends string>(props: ChoiceProps<T>) {
  return <Choices {...props} kind="radio" />;
}

export function Tabs<T extends string>({
  items,
  ...props
}: Omit<ChoiceProps<T>, "items"> & { items: readonly (ChoiceItem<T> & { panel?: ReactNode })[] }) {
  return <Choices {...props} items={items} kind="tab" />;
}

function Choices<T extends string>({
  label,
  items,
  value,
  onChange,
  className,
  kind,
}: Omit<ChoiceProps<T>, "items"> & {
  kind: "radio" | "tab";
  items: readonly (ChoiceItem<T> & { panel?: ReactNode })[];
}) {
  const id = useId();
  const buttons = useRef<(HTMLButtonElement | null)[]>([]);
  const selected = items.findIndex((item) => item.value === value && !item.disabled);
  const active = selected >= 0 ? selected : items.findIndex((item) => !item.disabled);
  return (
    <>
      <div
        className={[styles.group, kind === "tab" ? styles.tabs : styles.segmented, className]
          .filter(Boolean)
          .join(" ")}
        role={kind === "tab" ? "tablist" : "radiogroup"}
        aria-label={label}
      >
        {items.map((item, index) => (
          <button
            key={item.value}
            type="button"
            ref={(element) => {
              buttons.current[index] = element;
            }}
            id={`${id}-${index}`}
            role={kind}
            aria-selected={kind === "tab" ? item.value === value : undefined}
            aria-checked={kind === "radio" ? item.value === value : undefined}
            aria-controls={
              kind === "tab" && item.panel !== undefined ? `${id}-panel-${index}` : undefined
            }
            disabled={item.disabled}
            tabIndex={active === index ? 0 : -1}
            onClick={() => onChange(item.value)}
            onKeyDown={(event) => {
              const next = choiceIndex(
                event.key,
                index,
                items.map((entry) => !!entry.disabled),
              );
              if (next === undefined) return;
              event.preventDefault();
              onChange(items[next]!.value);
              buttons.current[next]?.focus();
            }}
          >
            {item.label}
          </button>
        ))}
      </div>
      {kind === "tab" &&
        items.map(
          (item, index) =>
            item.panel !== undefined && (
              <div
                key={item.value}
                id={`${id}-panel-${index}`}
                role="tabpanel"
                aria-labelledby={`${id}-${index}`}
                hidden={item.value !== value}
                tabIndex={0}
              >
                {item.panel}
              </div>
            ),
        )}
    </>
  );
}
