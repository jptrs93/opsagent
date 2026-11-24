import van from "vanjs-core";
import {createPresetStore} from "../lib/selectionPresets.js";

const browserStorage = () => {
    try {
        return globalThis.localStorage;
    } catch {
        return undefined;
    }
};

export function localPresets(key) {
    const store = createPresetStore({key, storage: browserStorage()});
    const list = van.state(store.list());
    if (typeof window !== "undefined") {
        window.addEventListener("storage", (event) => {
            if (event.key === key || event.key === null) list.val = store.reload();
        });
    }
    return {
        list,
        save: (name, ids) => {
            const saved = store.save(name, ids);
            list.val = store.list();
            return saved;
        },
        remove: (id) => {
            store.remove(id);
            list.val = store.list();
        },
    };
}

export const spacePresets = localPresets("opsagent_space_presets");
