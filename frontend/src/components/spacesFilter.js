import van from "vanjs-core";
import {checkIcon, chevronDownIcon, closeIcon} from "../lib/icons.js";
import {spaceHue} from "../lib/valueExplorer.js";
import {spacesS} from "../state/deployments.js";

const {button, div, span} = van.tags;

const spaceLabelOf = (space) => space?.name || `space ${space?.id ?? 0}`;
const setsEqual = (a, b) => a.size === b.size && [...a].every((id) => b.has(id));

export const spaceDot = (spaceId) => span({
    class: "inline-block w-[7px] h-[7px] rounded-full flex-none",
    style: `background:${spaceHue(spaceId)}`,
});

// spacesFilter is the multi-select space visibility dropdown shared with the
// status-page toolbar pattern: a dots + "N spaces" summary button over a
// checkbox menu of all spaces, with a reset row once the selection differs
// from defaultHidden. hiddenS holds the Set of hidden space ids; onChange
// fires with the new set after every edit (for persistence).
export function spacesFilter({hiddenS, defaultHidden = new Set(), onChange, buttonClass, testid = "spaces-filter"}) {
    const open = van.state(false);
    const ordered = () => [...(spacesS.val || [])].sort((a, b) => ((a.id === 0) - (b.id === 0)) || (a.id - b.id));
    const visible = () => ordered().filter((s) => !hiddenS.val.has(Number(s.id)));
    const dirty = () => !setsEqual(hiddenS.val, defaultHidden);

    const setHidden = (next) => {
        hiddenS.val = next;
        onChange?.(next);
    };

    const menuRow = (attrs, onclick, ...children) => button({
        type: "button",
        ...attrs,
        class: "flex items-center gap-2 rounded px-2 py-1.5 text-left text-xs text-gray-200 hover:bg-surface-hover cursor-pointer",
        onclick,
    }, ...children);

    const menu = () => div(
        {class: "absolute top-full left-0 z-30 mt-1.5 min-w-52 rounded-md border border-gray-600 bg-surface p-1 shadow-2xl flex flex-col"},
        ...ordered().map((space) => menuRow(
            {
                "data-testid": `${testid}-row-${space.id}`,
                role: "menuitemcheckbox",
                "aria-checked": String(!hiddenS.val.has(Number(space.id))),
            },
            () => {
                const next = new Set(hiddenS.val);
                next.has(Number(space.id)) ? next.delete(Number(space.id)) : next.add(Number(space.id));
                setHidden(next);
            },
            checkIcon({class: `w-3.5 h-3.5 flex-none text-brand ${hiddenS.val.has(Number(space.id)) ? "invisible" : ""}`}),
            spaceDot(space.id),
            span({class: "font-mono"}, spaceLabelOf(space)),
        )),
        ...(dirty() ? [
            div({class: "my-1 border-t border-gray-700"}),
            menuRow({}, () => setHidden(new Set(defaultHidden)),
                closeIcon({class: "w-3.5 h-3.5 flex-none text-brand"}), "Reset to default"),
        ] : []),
    );

    return span(
        {class: "relative inline-flex"},
        button({
            "data-testid": testid,
            type: "button",
            "aria-haspopup": "true",
            "aria-expanded": () => String(open.val),
            "aria-label": "Filter spaces",
            class: buttonClass || "inline-flex items-center gap-1.5 rounded px-2 py-1.5 text-xs cursor-pointer border border-gray-600 text-gray-300 hover:bg-surface-hover hover:text-gray-100 transition-colors",
            onclick: () => { open.val = !open.val; },
        }, () => span({class: "inline-flex items-center gap-1.5 whitespace-nowrap"},
            span({class: "inline-flex items-center gap-1"}, ...visible().map((s) => spaceDot(s.id))),
            `${visible().length} space${visible().length === 1 ? "" : "s"}`,
            chevronDownIcon({class: "w-3 h-3"}),
        )),
        () => open.val ? div(
            div({class: "fixed inset-0 z-20", onclick: () => { open.val = false; }}),
            menu(),
        ) : "",
    );
}
