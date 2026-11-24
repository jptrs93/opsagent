import van from "vanjs-core";
import {alertCircleIcon, checkIcon, eyeOffIcon, eyeOpenIcon, logoMark, shieldCheckIcon} from "../lib/icons.js";
import {themeCorner} from "./themePicker.js";

const {button, div, h1, input, label, main, span} = van.tags;

// Shared shell for the pages shown before a session exists (login and
// first-time setup): one centred card on the page backdrop, with a header
// line, a body, and a footer band. The look of every part is per theme, in
// styles/auth.css.

// The primary action of a page. The shape is passed as spinnerButton's base
// so its default padding and radius do not fight it.
export const authButtonBase = "flex w-full items-center justify-center px-4 py-2.5 text-sm font-medium";
export const authButtonClass = "auth-btn";

// A second way to do the same thing, offered next to a primary action.
export const authSecondaryButtonClass = "auth-btn-secondary";

export const authLinkClass = "auth-link cursor-pointer";

// Grouped fields: one bordered box, a hairline between rows, the label as a
// micro caption inside each row, one focus ring for the whole box.
export const fieldGroup = (...rows) => div({class: "auth-fields transition-colors"}, ...rows);

export const fieldRow = (text, inputEl, trailing) => label(
    {class: "auth-field flex cursor-text items-center gap-2"},
    div({class: "min-w-0 flex-1"},
        span({class: "auth-caption block"}, text),
        inputEl),
    trailing || '',
);

export const fieldInput = (attrs) => input({class: "auth-input w-full bg-transparent text-sm text-gray-100 focus:outline-none", ...attrs});

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

export const notice = (tone, testid, ...children) => div(
    {class: `auth-notice auth-notice-${tone} flex items-start gap-2 px-3 py-2.5 text-sm`, "data-testid": testid},
    (tone === "success" ? checkIcon : alertCircleIcon)({class: "auth-notice-icon mt-0.5 h-4 w-4 shrink-0"}),
    span({class: "min-w-0"}, ...children));

export const divider = (text) => div({class: "auth-divider flex items-center gap-3"},
    div({class: "auth-divider-rule h-px flex-1"}), text, div({class: "auth-divider-rule h-px flex-1"}));

// Footer band rows: a term, a value, and an optional trailing control.
export const footerRow = (term, value, trailing) => div(
    {class: "auth-foot-row flex items-center gap-3 px-4 py-2"},
    span({class: "auth-term w-20 shrink-0"}, term),
    span({class: "min-w-0 flex-1 truncate text-xs text-gray-300"}, value),
    trailing || '',
);

export const footerText = (...children) => div({class: "auth-foot-row auth-foot-text px-4 py-2 text-xs"}, ...children);

// The footer band itself; pages build it inside a binding when its rows
// follow discovered state.
export const footerBand = (...rows) => div({class: "auth-foot"}, ...rows);

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
            class: "auth-chip flex shrink-0 cursor-pointer items-center gap-1 px-2 py-0.5 text-[11px] transition-colors",
            "data-testid": testid,
            onclick: () => { caOpen.val = true; },
        }, shieldCheckIcon({class: "h-3 w-3"}), "Trust the CA")
        : '');
};

// The page: backdrop, centred card with header line (mark, "OpenDeploy",
// the page title), body children, and a footer band (a `footerBand` node or a
// binding that returns one), the theme cog, plus any overlays.
export const authCard = ({title, body, footer, overlays = [], width = "max-w-[420px]"}) => div(
    {class: "auth-page app-scroll relative h-dvh w-dvw overflow-y-auto"},
    // Classic's backdrop: a faint dot grid fading out from the top, under a
    // soft brand glow. Both sit at the top of the page and scroll with it.
    div({class: "auth-backdrop pointer-events-none absolute inset-x-0 top-0 h-dvh opacity-30",
        style: "background-image: radial-gradient(#334155 1px, transparent 1px); background-size: 22px 22px; mask-image: radial-gradient(ellipse at 50% 0%, black 0%, transparent 60%); -webkit-mask-image: radial-gradient(ellipse at 50% 0%, black 0%, transparent 60%);"}),
    div({class: "auth-glow pointer-events-none absolute left-1/2 top-[-14rem] h-[28rem] w-[40rem] -translate-x-1/2 rounded-full bg-brand/15 blur-3xl"}),
    main(
        {class: "auth-main relative flex min-h-full items-center justify-center p-4 pb-12 sm:p-10"},
        div(
            {class: () => `auth-card w-full ${typeof width === "function" ? width() : width} overflow-hidden`},
            div(
                {class: "auth-head"},
                h1({class: "auth-title flex items-center gap-2.5"},
                    logoMark({size: 24}), "OpenDeploy", span({class: "auth-title-page"}, title)),
            ),
            div({class: "flex flex-col gap-5 p-5"}, ...body),
            footer,
        ),
    ),
    themeCorner(),
    ...overlays,
);
