// Theme: "auto" follows the OS; dark or light pins it via data-theme on <html>.
export type Theme = "auto" | "dark" | "light";

const key = "ncli-dashboard:theme";
const listeners = new Set<() => void>();

export function savedTheme(): Theme {
  try {
    const t = localStorage.getItem(key);
    if (t === "dark" || t === "light") return t;
  } catch {
    /* storage blocked */
  }
  return "auto";
}

export function applyTheme(t: Theme, save = true) {
  const root = document.documentElement;
  if (t === "auto") delete root.dataset.theme;
  else root.dataset.theme = t;
  if (save) {
    try {
      if (t === "auto") localStorage.removeItem(key);
      else localStorage.setItem(key, t);
    } catch {
      /* storage blocked */
    }
  }
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
