import van from "vanjs-core";

const {div, input, ul, li, span} = van.tags;

const defaultInputClass = "w-full rounded-[0.3rem] bg-gray-800 border border-gray-700 px-1.5 py-1 text-gray-100 focus:outline-none focus:ring-1 focus:ring-brand";

function valueOf(value) {
    if (typeof value === 'function') return value();
    if (value && typeof value === 'object' && 'val' in value) return value.val;
    return value;
}

function normalizeKey(value) {
    if (value === undefined || value === null || value === 0) return '';
    return String(value);
}

export function referencePicker({
    refs,
    selectedKey,
    selectedLabel,
    onSelect,
    getKey = ref => ref.id,
    getLabel = ref => ref.name,
    placeholder = 'Search',
    noMatchesLabel = 'No matches',
    emptyLabel = 'No options available',
    inputClass = defaultInputClass,
    containerClass = "relative",
    disabled = false,
    maxMatches = 8,
}) {
    const search = van.state('');
    const searchDirty = van.state(false);
    const editing = van.state(false);
    const open = van.state(false);
    const highlightedIndex = van.state(0);

    const allRefs = () => valueOf(refs) || [];
    const currentKey = () => normalizeKey(valueOf(selectedKey));
    const refKey = ref => normalizeKey(getKey(ref));
    const currentRef = () => allRefs().find(ref => refKey(ref) === currentKey());
    const currentLabel = () => {
        const ref = currentRef();
        if (ref) return getLabel(ref) || '';
        return currentKey() ? (valueOf(selectedLabel) || '') : '';
    };
    const matches = () => {
        const query = searchDirty.val ? search.val.trim().toLowerCase() : '';
        const filtered = query
            ? allRefs().filter(ref => (getLabel(ref) || '').toLowerCase().includes(query))
            : allRefs();
        return filtered.slice(0, maxMatches);
    };
    const clampedHighlight = (items) => Math.max(0, Math.min(highlightedIndex.val, items.length - 1));
    const highlightSelected = () => {
        const items = matches();
        const index = items.findIndex(ref => refKey(ref) === currentKey());
        highlightedIndex.val = index >= 0 ? index : 0;
    };
    const choose = (ref) => {
        onSelect(ref);
        search.val = '';
        searchDirty.val = false;
        editing.val = false;
        open.val = false;
    };

    return div({class: containerClass},
        input({
            type: "text",
            class: inputClass,
            placeholder,
            disabled,
            value: () => editing.val && searchDirty.val ? search.val : currentLabel(),
            autocomplete: "off",
            onfocus: e => {
                search.val = '';
                searchDirty.val = false;
                editing.val = true;
                open.val = true;
                highlightSelected();
                setTimeout(() => e.target.select(), 0);
            },
            onblur: () => {
                setTimeout(() => {
                    search.val = '';
                    searchDirty.val = false;
                    editing.val = false;
                    open.val = false;
                }, 120);
            },
            oninput: e => {
                search.val = e.target.value;
                searchDirty.val = true;
                editing.val = true;
                open.val = true;
                highlightedIndex.val = 0;
            },
            onkeydown: e => {
                if (e.key === 'Escape') {
                    e.preventDefault();
                    e.currentTarget.blur();
                    return;
                }
                if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
                    e.preventDefault();
                    if (!open.val) open.val = true;
                    const matchingItems = matches();
                    if (matchingItems.length === 0) return;
                    const current = clampedHighlight(matchingItems);
                    highlightedIndex.val = e.key === 'ArrowDown'
                        ? (current + 1) % matchingItems.length
                        : (current - 1 + matchingItems.length) % matchingItems.length;
                    return;
                }
                if (e.key !== 'Enter' || !open.val) return;
                e.preventDefault();
                const items = matches();
                const selected = items[clampedHighlight(items)];
                if (selected) choose(selected);
            },
        }),
        () => {
            if (!open.val || valueOf(disabled)) return '';
            const items = matches();
            return ul(
                {class: "absolute z-50 mt-1 max-h-44 w-full overflow-auto rounded-md border border-gray-700 bg-gray-900 shadow-xl"},
                ...(items.length === 0
                    ? [li({class: "px-2 py-1.5 text-gray-500"}, allRefs().length ? noMatchesLabel : emptyLabel)]
                    : items.map((ref, index) => li({
                        class: () => `flex items-center justify-between gap-2 cursor-pointer px-2 py-1.5 bg-gray-800 text-gray-200 hover:bg-gray-700 ${index === clampedHighlight(items) ? 'bg-gray-700 text-white' : ''}`,
                        onmouseenter: () => { highlightedIndex.val = index; },
                        onmousedown: e => {
                            e.preventDefault();
                            choose(ref);
                        },
                    },
                        span({class: "truncate"}, getLabel(ref) || ''),
                        () => index === clampedHighlight(items)
                            ? span({class: "shrink-0 text-[10px] tracking-wide text-blue-300"}, "Enter to select")
                            : refKey(ref) === currentKey()
                                ? span({class: "shrink-0 text-[10px] tracking-wide text-gray-400"}, "Selected")
                                : '',
                    ))),
            );
        },
    );
}
