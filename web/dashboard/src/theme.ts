import { load, save } from "./persist";

// Theme: "auto" follows the OS; dark or light pins it via data-theme on <html>.
export type Theme = "auto" | "dark" | "light";

const listeners = new Set<() => void>();

export function savedTheme(): Theme {
  const t = load("theme");
  return t === "dark" || t === "light" ? t : "auto";
}

export function applyTheme(t: Theme, keep = true) {
  const root = document.documentElement;
  if (t === "auto") delete root.dataset.theme;
  else root.dataset.theme = t;
  if (keep) save("theme", t === "auto" ? null : t);
  listeners.forEach((fn) => fn());
}

// onThemeChange fires when the pinned theme or the OS scheme changes.
export function onThemeChange(fn: () => void): () => void {
  const mq = matchMedia("(prefers-color-scheme: dark)");
  listeners.add(fn);
  mq.addEventListener("change", fn);
  return () => {
    listeners.delete(fn);
    mq.removeEventListener("change", fn);
  };
}
