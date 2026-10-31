// Light and dark themes: body[data-dark] switches the tokens. The page
// follows the system until the owner picks a theme, which this browser
// keeps. public/theme-init.js applies the same rule before the first paint.
import { Store, type Readable } from "../state/store.ts";

export type ThemeChoice = "system" | "light" | "dark";

const key = "tend.theme";
const query = "(prefers-color-scheme: dark)";

export const themeLabels: Record<ThemeChoice, string> = {
  system: "跟随系统",
  light: "浅色",
  dark: "深色",
};

function saved(): ThemeChoice {
  try {
    const v = localStorage.getItem(key);
    return v === "light" || v === "dark" ? v : "system";
  } catch {
    return "system";
  }
}

const choice = new Store<ThemeChoice>(saved());
const media = window.matchMedia(query);

function apply(): void {
  const c = choice.get();
  document.body.toggleAttribute("data-dark", c === "dark" || (c === "system" && media.matches));
}

media.addEventListener("change", apply);
apply();

export const theme: Readable<ThemeChoice> = choice;

/** Persist and apply an explicit preference from the settings page. */
export function setTheme(c: ThemeChoice): void {
  try {
    if (c === "system") {
      localStorage.removeItem(key);
    } else {
      localStorage.setItem(key, c);
    }
  } catch {
    // Not kept, but still applied.
  }
  choice.set(c);
  apply();
}
