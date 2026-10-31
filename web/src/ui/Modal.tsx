import { rememberOpener } from "./focus.ts";
import { useId, useLayoutEffect, useRef, type DialogHTMLAttributes } from "react";
import { Button } from "./Button.tsx";
import styles from "./Modal.module.css";

type ModalProps = Omit<DialogHTMLAttributes<HTMLDialogElement>, "open" | "onClose" | "title"> & {
  open: boolean;
  title: string;
  onClose(): void;
};

export function Dialog(props: ModalProps) {
  return <Modal {...props} sheet={false} />;
}
export function Sheet(props: ModalProps) {
  return <Modal {...props} sheet />;
}

function outsidePanel(panel: HTMLDialogElement, x: number, y: number) {
  const bounds = panel.getBoundingClientRect();
  return x < bounds.left || x > bounds.right || y < bounds.top || y > bounds.bottom;
}

function Modal({
  open,
  title,
  onClose,
  children,
  className,
  onCancel,
  onClick,
  sheet,
  ...props
}: ModalProps & { sheet: boolean }) {
  const id = useId();
  const dialog = useRef<HTMLDialogElement>(null);
  const closing = useRef(false);
  const backdrop = useRef(false);
  useLayoutEffect(() => {
    if (!open) return;
    const panel = dialog.current!;
    const opener = document.activeElement;
    rememberOpener(panel, opener);
    closing.current = false;
    if (!panel.open) panel.showModal();
    return () => {
      closing.current = true;
      panel.close();
      if (opener instanceof HTMLElement && opener.isConnected)
        opener.focus({ preventScroll: true });
    };
  }, [open]);
  return (
    <dialog
      {...props}
      ref={dialog}
      aria-labelledby={props["aria-labelledby"] ?? (props["aria-label"] ? undefined : id)}
      className={[styles.modal, sheet ? styles.sheet : styles.dialog, className]
        .filter(Boolean)
        .join(" ")}
      onClose={(event) => {
        event.stopPropagation();
        if (!closing.current && !event.currentTarget.open) onClose();
      }}
      onCancel={(event) => {
        event.stopPropagation();
        onCancel?.(event);
        if (event.defaultPrevented) return;
        event.preventDefault();
        onClose();
      }}
      onPointerDown={(event) => {
        backdrop.current =
          event.target === event.currentTarget &&
          outsidePanel(event.currentTarget, event.clientX, event.clientY);
        props.onPointerDown?.(event);
      }}
      onClick={(event) => {
        onClick?.(event);
        if (
          !event.defaultPrevented &&
          backdrop.current &&
          event.target === event.currentTarget &&
          outsidePanel(event.currentTarget, event.clientX, event.clientY)
        )
          onClose();
        backdrop.current = false;
      }}
    >
      <header className={styles.header}>
        <h2 id={id}>{title}</h2>
        <Button variant="ghost" icon aria-label={`关闭${title}`} onClick={onClose}>
          ×
        </Button>
      </header>
      {children}
    </dialog>
  );
}
