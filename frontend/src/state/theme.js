import van from "vanjs-core";

export const THEME_KEY = "opsagent_theme";
export const DEFAULT_THEME = "slate";

export const THEMES = [
    {key: "slate", label: "Slate", frame: "#0c1118", surface: "#1d2633"},
    {key: "graphite", label: "Graphite", frame: "#24272c", surface: "#111214"},
    {key: "classic", label: "Classic", frame: "#111827", surface: "#1f2937"},
];

const known = (key) => THEMES.some((theme) => theme.key === key);

const readTheme = () => {
    try {
        const stored = localStorage.getItem(THEME_KEY);
        if (known(stored)) return stored;
    } catch {}
    return DEFAULT_THEME;
};

const applyTheme = (key) => {
    if (typeof document !== "undefined") document.documentElement.dataset.theme = key;
};

export const themeS = van.state(readTheme());

applyTheme(themeS.val);

export const setTheme = (key) => {
    if (!known(key)) return;
    themeS.val = key;
    applyTheme(key);
    try { localStorage.setItem(THEME_KEY, key); } catch {}
};
