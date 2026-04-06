import van from "vanjs-core";

export const currentPath = van.state(window.location.pathname);

window.addEventListener("popstate", () => {
    currentPath.val = window.location.pathname;
});

export function navigate(path, {replace = false} = {}) {
    if (replace) {
        window.history.replaceState({}, "", path);
    } else {
        window.history.pushState({}, "", path);
    }
    currentPath.val = path;
}
