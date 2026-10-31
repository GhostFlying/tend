import {
  cloneElement,
  forwardRef,
  useId,
  type InputHTMLAttributes,
  type ReactElement,
  type ReactNode,
  type SelectHTMLAttributes,
  type TextareaHTMLAttributes,
} from "react";
import styles from "./Field.module.css";

type FieldControl = {
  id?: string;
  "aria-describedby"?: string | undefined;
  "aria-invalid"?: InputHTMLAttributes<HTMLInputElement>["aria-invalid"];
};

export function Field({
  id,
  label,
  hint,
  error,
  children,
  className,
}: {
  id?: string;
  label: ReactNode;
  hint?: ReactNode;
  error?: ReactNode;
  children: ReactElement<FieldControl>;
  className?: string;
}) {
  const generatedId = useId();
  const controlId = id ?? children.props.id ?? generatedId;
  const description = [
    children.props["aria-describedby"],
    hint && `${controlId}-hint`,
    error && `${controlId}-error`,
  ]
    .filter(Boolean)
    .join(" ");
  return (
    <div className={[styles.field, className].filter(Boolean).join(" ")}>
      <label htmlFor={controlId}>{label}</label>
      {cloneElement(children, {
        id: controlId,
        "aria-describedby": description || undefined,
        "aria-invalid": error ? true : children.props["aria-invalid"],
      })}
      {hint && (
        <div id={`${controlId}-hint`} className={styles.hint}>
          {hint}
        </div>
      )}
      {error && (
        <div id={`${controlId}-error`} className={styles.error} role="alert">
          {error}
        </div>
      )}
    </div>
  );
}

export const Input = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement>>(
  function Input({ className, ...props }, ref) {
    return (
      <input
        {...props}
        ref={ref}
        className={[styles.control, className].filter(Boolean).join(" ")}
      />
    );
  },
);
export const Textarea = forwardRef<
  HTMLTextAreaElement,
  TextareaHTMLAttributes<HTMLTextAreaElement>
>(function Textarea({ className, ...props }, ref) {
  return (
    <textarea
      {...props}
      ref={ref}
      className={[styles.control, styles.textarea, className].filter(Boolean).join(" ")}
    />
  );
});
export const Select = forwardRef<HTMLSelectElement, SelectHTMLAttributes<HTMLSelectElement>>(
  function Select({ className, ...props }, ref) {
    return (
      <select
        {...props}
        ref={ref}
        className={[styles.control, className].filter(Boolean).join(" ")}
      />
    );
  },
);
export const Checkbox = forwardRef<
  HTMLInputElement,
  Omit<InputHTMLAttributes<HTMLInputElement>, "type"> & { label: ReactNode }
>(function Checkbox({ label, className, ...props }, ref) {
  return (
    <label className={[styles.checkbox, className].filter(Boolean).join(" ")}>
      <input {...props} ref={ref} type="checkbox" />
      {label}
    </label>
  );
});
