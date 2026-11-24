import van from "vanjs-core";
import {capi} from "/src/capi/index.js";
import {sidebar} from "/src/components/sidebar.js";
import {deploymentsStreamS} from "/src/state/deployments.js";

const {div} = van.tags;

capi.postV1GlobalEventStream = async function* (_, {signal} = {}) {
    while (!signal?.aborted) {
        yield {heartbeat: true};
        await new Promise((resolve) => {
            const timer = setTimeout(resolve, 3000);
            signal?.addEventListener("abort", () => { clearTimeout(timer); resolve(); }, {once: true});
        });
    }
    throw new DOMException("aborted", "AbortError");
};

export const shell = (content) => {
    deploymentsStreamS.val = {...deploymentsStreamS.rawVal, status: "connected"};
    return div(
        {class: "h-dvh min-h-dvh w-dvw flex overflow-hidden"},
        sidebar(van.state("users")),
        div({class: "h-full flex-1 min-w-0 min-h-0 overflow-hidden"}, content),
    );
};
