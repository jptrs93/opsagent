import van from "vanjs-core";
import {spaceHue} from "../lib/valueExplorer.js";
import {spacesS} from "../state/deployments.js";
import {spacePresets} from "../state/selectionPresets.js";
import {multiSelect} from "./multiSelect.js";

const {span} = van.tags;

const spaceLabelOf = (space) => space?.name || `space ${space?.id ?? 0}`;

const allSpaces = () => [...(spacesS.val || [])].sort((a, b) => ((a.id === 0) - (b.id === 0)) || (a.id - b.id));

export const spaceDot = (spaceId) => span({
    class: "inline-block w-[7px] h-[7px] rounded-full flex-none",
    style: `background:${spaceHue(spaceId)}`,
});

export const spaceTag = (spaceId, name) => span({class: "space-tag"},
    spaceDot(spaceId),
    span({class: "space-name truncate"}, name));

// The pages keep the set of hidden space ids, so a space created later shows
// without anyone touching the filter. The widget works on the shown set, and a
// preset is a shown set: applying one hides every space it does not name.
export function spacesFilter({
    hiddenS,
    hidden = () => hiddenS.val,
    spaces = allSpaces,
    defaultHidden = new Set(),
    onChange,
    onReset,
    tail,
    buttonClass = "metrics-control",
    testid = "spaces-filter",
}) {
    const defaults = typeof defaultHidden === "function" ? defaultHidden : () => defaultHidden;
    const listedIds = () => spaces().map((space) => Number(space.id));
    const shownOf = (hiddenIds) => new Set(listedIds().filter((id) => !hiddenIds.has(id)));

    const setHidden = (next) => {
        if (hiddenS) hiddenS.val = next;
        onChange?.(next);
    };

    return multiSelect({
        items: () => spaces().map((space) => ({
            id: Number(space.id),
            label: spaceLabelOf(space),
            lead: () => spaceDot(space.id),
            tail: tail ? tail(space) : "",
        })),
        value: () => shownOf(hidden()),
        defaultValue: () => shownOf(defaults()),
        onChange: (shown) => {
            const listed = listedIds();
            const next = new Set([...hidden()].filter((id) => !listed.includes(id)));
            for (const id of listed) if (!shown.has(id)) next.add(id);
            setHidden(next);
        },
        onReset: onReset || (() => setHidden(new Set(defaults()))),
        presets: spacePresets,
        noun: ["space", "spaces"],
        ariaLabel: "Filter spaces",
        labelClass: "space-name",
        buttonClass,
        testid,
    });
}
