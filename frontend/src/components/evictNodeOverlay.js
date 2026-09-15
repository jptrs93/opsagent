import van from "vanjs-core";
import {capi} from "../capi/index.js";

const {button, div, h2, input, label, li, p, span, ul} = van.tags;

const SYSTEM_SPACE_ID = 0;

export function pinnedUserDeployments(deployments, nodeId) {
    return (deployments || []).filter(row => Number(row.config?.value?.nodeId) === Number(nodeId)
        && Number(row.config?.value?.spaceId) !== SYSTEM_SPACE_ID);
}

const itemLabel = item => `${item.name || `#${item.id}`}${item.version ? ` v${item.version}` : ''}`;

const exposureLine = (title, items) => items.length === 0 ? '' : li(
    span({class: "text-gray-400"}, `${title}: `),
    span({class: "text-gray-200 break-words"}, items.join(", ")),
);

function exposureSummary(exposure) {
    if (!exposure) return '';
    const lines = [
        exposureLine("Deployments", (exposure.deployments || []).map(itemLabel)),
        exposureLine("Secrets", (exposure.secrets || []).map(itemLabel)),
        exposureLine("Configs", (exposure.configs || []).map(itemLabel)),
        exposureLine("Issued TLS certificates", (exposure.issuedTlsDeployments || []).map(itemLabel)),
        exposureLine("ACME certificates", exposure.acmeHostnames || []),
        exposureLine("Credentials", exposure.githubToken ? ["GitHub token"] : []),
    ].filter(Boolean);
    if (lines.length === 0) return p({class: "text-sm text-gray-400"}, "No cluster data has been delivered to this node.");
    return div({class: "flex flex-col gap-1"},
        p({class: "text-sm text-gray-300"}, "Data this node has held. Rotate anything below that you no longer trust the machine with."),
        ul({class: "text-xs flex flex-col gap-0.5 pl-4 list-disc"}, ...lines));
}

export function evictNodeOverlay({machine, pinned, evict, close}) {
    const exposure = van.state(null);
    const loadError = van.state('');
    const loading = van.state(true);
    const saving = van.state(false);
    const error = van.state('');
    const force = van.state(false);
    const name = machine.name || machine.identifier;
    const blocked = pinned.length > 0;

    capi.postV1NodesExposure({identifier: machine.identifier})
        .then(result => { exposure.val = result; })
        .catch(e => { loadError.val = e?.message || 'Loading node exposure failed.'; })
        .finally(() => { loading.val = false; });

    const confirm = async () => {
        if (saving.val || (blocked && !force.val)) return;
        error.val = '';
        saving.val = true;
        try {
            await evict(force.val);
            close();
        } catch (e) {
            error.val = e?.message || 'Evicting node failed.';
        } finally {
            saving.val = false;
        }
    };

    return div(
        {class: "fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4", "data-testid": "node-evict-overlay"},
        div(
            {class: "card w-full max-w-lg flex flex-col gap-4 shadow-2xl"},
            h2({class: "text-base font-semibold"}, `Evict ${name}`),
            p({class: "text-sm text-gray-300"},
                "Eviction permanently removes this node from the cluster. The node stops its workloads, wipes the cluster data it holds and refuses to start again. Its identity can never re-enroll; to use the machine again, reinstall the secondary."),
            () => loading.val
                ? p({class: "text-sm text-gray-400"}, "Loading what this node has held...")
                : loadError.val
                    ? p({class: "text-sm text-red-400"}, loadError.val)
                    : exposureSummary(exposure.val),
            !blocked ? '' : div(
                {class: "rounded border border-amber-600/60 bg-amber-900/20 p-3 flex flex-col gap-2", "data-testid": "node-evict-warning"},
                p({class: "text-sm text-amber-200"},
                    `${pinned.length} deployment${pinned.length === 1 ? '' : 's'} still target${pinned.length === 1 ? 's' : ''} this node. Evicting stops ${pinned.length === 1 ? 'it' : 'them'} and leaves ${pinned.length === 1 ? 'it' : 'them'} pinned to the evicted node until you move ${pinned.length === 1 ? 'it' : 'them'}.`),
                ul({class: "text-xs text-amber-100 pl-4 list-disc"},
                    ...pinned.map(row => li(`${row.config.value.name} (#${row.config.deploymentId})`))),
                label({class: "flex items-center gap-2 text-sm text-gray-200 cursor-pointer"},
                    input({type: "checkbox", "data-testid": "node-evict-force", checked: () => force.val, onchange: e => { force.val = e.target.checked; }}),
                    "Evict anyway and stop these deployments"),
            ),
            () => error.val ? p({class: "text-sm text-red-400"}, error.val) : '',
            div({class: "flex items-center justify-end gap-2"},
                button({
                    type: "button",
                    class: "text-xs px-3 py-1 rounded-md font-medium bg-gray-700 text-gray-200 hover:bg-gray-600 disabled:opacity-60 cursor-pointer",
                    disabled: () => saving.val,
                    onclick: close,
                }, "Cancel"),
                button({
                    type: "button",
                    class: "text-xs px-3 py-1 rounded-md font-medium bg-red-700 text-white hover:bg-red-600 disabled:opacity-60 disabled:cursor-not-allowed cursor-pointer",
                    disabled: () => saving.val || (blocked && !force.val),
                    "data-testid": "node-evict-confirm",
                    onclick: confirm,
                }, () => saving.val ? "Evicting..." : "Evict node"),
            ),
        ),
    );
}
