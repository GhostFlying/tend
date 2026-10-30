import { forwardRef, type ButtonHTMLAttributes } from "react";
import styles from "./Button.module.css";

export type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: "primary" | "secondary" | "ghost" | "danger" | "link";
  size?: "s" | "m" | "l";
  icon?: boolean;
};

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { variant = "secondary", size = "m", icon = false, type = "button", className, ...props },
  ref,
) {
  return (
    <button
      {...props}
      ref={ref}
      type={type}
      className={[styles.button, styles[variant], styles[size], icon && styles.icon, className]
        .filter(Boolean)
        .join(" ")}
    />
  );
});
