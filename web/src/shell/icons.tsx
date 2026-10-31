// Line icons from the design mock, drawn with the current text color.
import type { SVGProps } from "react";

type IconProps = SVGProps<SVGSVGElement>;

const line = { fill: "none", stroke: "currentColor", strokeWidth: 2, "aria-hidden": true } as const;

export const ArrowDown = (p: IconProps) => (
  <svg viewBox="0 0 24 24" {...line} {...p}>
    <path d="M12 5v14M5 12l7 7 7-7" />
  </svg>
);

export const Close = (p: IconProps) => (
  <svg viewBox="0 0 24 24" {...line} {...p}>
    <path d="M6 6l12 12M18 6L6 18" />
  </svg>
);

export const Back = (p: IconProps) => (
  <svg viewBox="0 0 24 24" {...line} {...p}>
    <path d="M15 6l-6 6 6 6" />
  </svg>
);

export const Send = (p: IconProps) => (
  <svg viewBox="0 0 24 24" {...line} {...p}>
    <path d="M12 19V5M5 12l7-7 7 7" />
  </svg>
);

export const Chat = (p: IconProps) => (
  <svg viewBox="0 0 24 24" {...line} strokeWidth={1.8} {...p}>
    <path d="M4 5h16v11H9l-5 4z" />
  </svg>
);

export const Tasks = (p: IconProps) => (
  <svg viewBox="0 0 24 24" {...line} strokeWidth={1.8} {...p}>
    <path d="M9 6h11M9 12h11M9 18h11M4 6h.01M4 12h.01M4 18h.01" />
  </svg>
);

export const Chevron = (p: IconProps) => (
  <svg viewBox="0 0 24 24" {...line} {...p}>
    <path d="M9 6l6 6-6 6" />
  </svg>
);

export const Stop = (p: IconProps) => (
  <svg viewBox="0 0 24 24" fill="currentColor" aria-hidden {...p}>
    <rect x="7" y="7" width="10" height="10" rx="2" />
  </svg>
);

export const Pending = (p: IconProps) => (
  <svg viewBox="0 0 24 24" {...line} strokeWidth={1.8} {...p}>
    <circle cx="12" cy="12" r="8" />
    <path d="M12 8v5M12 16h.01" />
  </svg>
);

export const Gear = (p: IconProps) => (
  <svg viewBox="0 0 24 24" {...line} {...p}>
    <path d="m9 3-1 3-3 1 1 3-2 2 2 2-1 3 3 1 1 3h4l1-3 3-1-1-3 2-2-2-2 1-3-3-1-1-3z" />
    <circle cx="11" cy="12" r="3" />
  </svg>
);

export const Tag = (p: IconProps) => (
  <svg viewBox="0 0 24 24" {...line} strokeWidth={1.6} {...p}>
    <path d="M3 4h8l10 10-7 7L3 10z" strokeLinejoin="round" />
    <circle cx="7.5" cy="7.5" r="1" />
  </svg>
);

export const ExternalLink = (p: IconProps) => (
  <svg viewBox="0 0 24 24" {...line} strokeWidth={1.6} {...p}>
    <path d="M8 16L19 5M11 5h8v8" strokeLinecap="round" strokeLinejoin="round" />
  </svg>
);

export const Repository = (p: IconProps) => (
  <svg viewBox="0 0 24 24" {...line} {...p}>
    <path d="M4 5h6l2 2h8v12H4z" />
  </svg>
);
export const Usage = (p: IconProps) => (
  <svg viewBox="0 0 24 24" {...line} {...p}>
    <path d="M4 20V4M4 20h16M8 16v-5M13 16V7M18 16v-8" />
  </svg>
);
export const Host = (p: IconProps) => (
  <svg viewBox="0 0 24 24" {...line} {...p}>
    <rect x="4" y="4" width="16" height="6" rx="2" />
    <rect x="4" y="14" width="16" height="6" rx="2" />
    <path d="M8 7h.01M8 17h.01" />
  </svg>
);
export const More = (p: IconProps) => (
  <svg viewBox="0 0 24 24" {...line} {...p}>
    <circle cx="5" cy="12" r="1" />
    <circle cx="12" cy="12" r="1" />
    <circle cx="19" cy="12" r="1" />
  </svg>
);
export const ChevronDown = (p: IconProps) => (
  <svg viewBox="0 0 24 24" {...line} {...p}>
    <path d="m6 9 6 6 6-6" />
  </svg>
);
