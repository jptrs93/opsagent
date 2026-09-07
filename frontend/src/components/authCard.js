import van from "vanjs-core";
import {alertCircleIcon, checkIcon, eyeOffIcon, eyeOpenIcon, logoMark, shieldCheckIcon} from "../lib/icons.js";

const {button, div, h1, input, label, main, span} = van.tags;

// Shared shell for the pages shown before a session exists (login and
// first-time setup): one centred card on a dot-grid backdrop, with a header
// line, a body, and a footer band.

// One button style for every action on these pages, between a filled primary
// and an outlined secondary: a brand tint with a brand border. The shape is
// passed as spinnerButton's base so its default padding and radius do not
// fight it.
export const authButtonBase = "flex w-full items-center justify-center rounded-lg px-4 py-2.5 text-sm font-medium";
export const authButtonClass = "border border-brand/40 bg-brand/15 text-blue-100 hover:border-brand/70 hover:bg-brand/25";

export const authLinkClass = "cursor-pointer text-brand hover:text-blue-400";

// Grouped fields: one bordered box, a hairline between rows, the label as a
// micro caption inside each row, one focus ring for the whole box.
export const fieldGroup = (...rows) => div(
    {class: "divide-y divide-gray-700 rounded-lg border border-gray-700 bg-gray-900/60 transition-colors focus-within:border-brand focus-within:ring-2 focus-within:ring-brand/25"},
    ...rows,
);

export const fieldRow = (text, inputEl, trailing) => label(
    {class: "flex cursor-text items-center gap-2 px-3 py-2"},
    div({class: "min-w-0 flex-1"},
        span({class: "block text-[10px] uppercase tracking-wide text-gray-500"}, text),
        inputEl),
    trailing || '',
);

export const fieldInput = (attrs) => input({class: "w-full bg-transparent text-sm text-gray-100 placeholder-gray-600 focus:outline-none", ...attrs});

// Eye toggle for a password input, placed as a field row's trailing control.
export const revealToggle = (passwordInput) => {
    const shown = van.state(false);
    return button({
        type: "button",
        tabindex: -1,
        title: () => shown.val ? "Hide password" : "Show password",
        class: "shrink-0 cursor-pointer text-gray-500 transition-colors hover:text-gray-200",
        onclick: () => { shown.val = !shown.val; passwordInput.type = shown.val ? "text" : "password"; },
    }, () => shown.val ? eyeOffIcon({class: "h-4 w-4"}) : eyeOpenIcon({class: "h-4 w-4"}));
};

export const withIcon = (icon, text) => span({class: "inline-flex items-center gap-2"}, icon, text);

const noticeTones = {
    error: "border-red-900/60 bg-red-950/40 text-red-300",
    warning: "border-amber-900/50 bg-amber-950/30 text-amber-200/90",
    success: "border-emerald-900/60 bg-emerald-950/30 text-emerald-200/90",
};

export const notice = (tone, testid, ...children) => div(
    {class: `flex items-start gap-2 rounded-lg border px-3 py-2.5 text-sm ${noticeTones[tone]}`, "data-testid": testid},
    (tone === "success" ? checkIcon : alertCircleIcon)({class: "mt-0.5 h-4 w-4 shrink-0"}),
    span({class: "min-w-0"}, ...children));

export const divider = (text) => div({class: "flex items-center gap-3 text-[10px] uppercase tracking-wide text-gray-500"},
    div({class: "h-px flex-1 bg-gray-700/80"}), text, div({class: "h-px flex-1 bg-gray-700/80"}));

// Footer band rows: a term, a value, and an optional trailing control.
export const footerRow = (term, value, trailing) => div(
    {class: "flex items-center gap-3 px-4 py-2"},
    span({class: "w-20 shrink-0 text-[11px] uppercase tracking-wide text-gray-500"}, term),
    span({class: "min-w-0 flex-1 truncate text-xs text-gray-300"}, value),
    trailing || '',
);

export const footerText = (...children) => div({class: "px-4 py-2 text-xs text-gray-500"}, ...children);

// The footer band itself; pages build it inside a binding when its rows
// follow discovered state.
export const footerBand = (...rows) => div({class: "divide-y divide-gray-800 border-t border-gray-700/80 bg-gray-900/40"}, ...rows);

// The transport row: plain HTTP, HTTPS, or HTTPS under the local CA, with the
// "Trust the CA" action when the server can hand out its CA certificate.
export const transportRow = (methods, caOpen, testid) => {
    const dot = (color) => span({class: `h-1.5 w-1.5 shrink-0 rounded-full ${color}`});
    const secure = window.location.protocol === "https:";
    const transport = secure
        ? (methods.localCaAvailable
            ? span({class: "flex items-center gap-1.5"}, dot("bg-amber-400"), "HTTPS · local CA")
            : span({class: "flex items-center gap-1.5"}, dot("bg-emerald-400"), "HTTPS"))
        : span({class: "flex items-center gap-1.5"}, dot("bg-gray-500"), "Plain HTTP");
    return footerRow("Transport", transport, methods.localCaAvailable
        ? button({
            type: "button",
            class: "flex shrink-0 cursor-pointer items-center gap-1 rounded-md border border-gray-700 px-2 py-0.5 text-[11px] text-gray-300 transition-colors hover:border-gray-600 hover:bg-gray-800 hover:text-white",
            "data-testid": testid,
            onclick: () => { caOpen.val = true; },
        }, shieldCheckIcon({class: "h-3 w-3"}), "Trust the CA")
        : '');
};

// The page: backdrop, centred card with header line (mark, "OpenDeploy",
// the page title), body children, and a footer band (a `footerBand` node or a
// binding that returns one), plus any overlays.
export const authCard = ({title, body, footer, overlays = []}) => div(
    {class: "app-scroll relative h-dvh w-dvw overflow-y-auto bg-gray-950"},
    // Backdrop: a faint dot grid fading out from the top, under a soft brand
    // glow. Both sit at the top of the page and scroll with it.
    div({class: "pointer-events-none absolute inset-x-0 top-0 h-dvh opacity-30",
        style: "background-image: radial-gradient(#334155 1px, transparent 1px); background-size: 22px 22px; mask-image: radial-gradient(ellipse at 50% 0%, black 0%, transparent 60%); -webkit-mask-image: radial-gradient(ellipse at 50% 0%, black 0%, transparent 60%);"}),
    div({class: "pointer-events-none absolute left-1/2 top-[-14rem] h-[28rem] w-[40rem] -translate-x-1/2 rounded-full bg-brand/15 blur-3xl"}),
    main(
        {class: "relative flex min-h-full items-center justify-center p-4 sm:p-10"},
        div(
            {class: "w-full max-w-[420px] overflow-hidden rounded-xl border border-gray-700/80 bg-surface shadow-2xl shadow-black/60"},
            div(
                {class: "border-b border-gray-700/80 px-5 py-3.5"},
                h1({class: "flex items-center gap-2.5 text-lg font-semibold tracking-tight text-white"},
                    logoMark({size: 24}), "OpenDeploy", span({class: "font-normal text-gray-500"}, title)),
            ),
            div({class: "flex flex-col gap-5 p-5"}, ...body),
            footer,
        ),
    ),
    ...overlays,
);
