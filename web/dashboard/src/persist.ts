// Per-viewer preferences in localStorage; blocked storage just forgets.
const prefix = "ncli-dashboard:";

export function load(key: string): string | null {
  try {
    return localStorage.getItem(prefix + key);
  } catch {
    return null;
  }
}

export function save(key: string, value: string | null) {
  try {
    if (value == null) localStorage.removeItem(prefix + key);
    else localStorage.setItem(prefix + key, value);
  } catch {
    /* storage blocked */
  }
}
