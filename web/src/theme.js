// Theme preference: "auto" follows the operating system, "light" and
// "dark" force a palette. The chosen mode lives in localStorage; the
// tiny public/theme-init.js applies it before the first paint.

const STORAGE_KEY = "polka-theme";
const THEMES = ["auto", "light", "dark"];

const systemDark = () =>
  typeof window !== "undefined" && window.matchMedia
    ? window.matchMedia("(prefers-color-scheme: dark)")
    : null;

export const getTheme = () => {
  try {
    const stored = localStorage.getItem(STORAGE_KEY);
    if (THEMES.includes(stored)) return stored;
  } catch {
    /* ignore */
  }
  return "auto";
};

const effectiveTheme = (mode = getTheme()) =>
  mode === "auto" ? (systemDark()?.matches ? "dark" : "light") : mode;

const updateThemeColor = (mode) => {
  const meta = document.querySelector('meta[name="theme-color"]');
  if (meta) meta.setAttribute("content", effectiveTheme(mode) === "dark" ? "#121212" : "#f6f4ee");
};

const applyTheme = (mode = getTheme()) => {
  const next = THEMES.includes(mode) ? mode : "auto";
  document.documentElement.dataset.theme = next;
  updateThemeColor(next);
  return next;
};

export const setTheme = (mode) => {
  const next = THEMES.includes(mode) ? mode : "auto";
  try {
    localStorage.setItem(STORAGE_KEY, next);
  } catch {
    /* ignore */
  }
  return applyTheme(next);
};

// auto → light → dark → auto
const nextTheme = (mode = getTheme()) => {
  const index = THEMES.indexOf(mode);
  return THEMES[(index + 1) % THEMES.length];
};

export const cycleTheme = () => setTheme(nextTheme());

// Called once before rendering. The system listener keeps an "auto"
// choice in sync when the OS switches between light and dark.
export const initTheme = () => {
  const mode = applyTheme(getTheme());
  systemDark()?.addEventListener?.("change", () => {
    if (getTheme() === "auto") applyTheme("auto");
  });
  return mode;
};
