import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {machinesS, spacesS} from "../state/deployments.js";
import {nodeDisplayName} from "../lib/machines.js";
import {resolveUserDisplayName} from "../lib/users.js";
import {formatHistoryTime} from "../lib/date.js";
import {deploymentWorkload, placementNodeId} from "../lib/deployment.js";

const {div, h2, p, button, table, thead, tbody, tr, th, td} = van.tags;

const spaceName = (spaceId) => {
    const space = (spacesS.val || []).find(s => Number(s.id) === Number(spaceId));
    return space?.name || `space ${spaceId ?? 0}`;
};

const sourceLabel = (config) => {
    const source = deploymentWorkload(config)?.source?.value;
    return source?.nixImageBuild?.repo || source?.remoteImage?.image || '';
};

/**
 * recentlyDeletedOverlay lists the deployments deleted most recently and offers
 * to fork one back.
 *
 * Fork rather than restore: deleting releases the identity tuple but keeps the
 * id, and the id owns the volumes, logs, and address. Resurrecting it would
 * silently re-attach all of that, so a recovered deployment is a new deployment
 * seeded from the old config — which is what the create path already does.
 *
 * @param {(record: object) => void} onFork called with the deleted DeploymentRecord to seed a new deployment
 * @param {() => void} onClose
 */
export function recentlyDeletedOverlay(onFork, onClose) {
    const items = van.state(null);
    const error = van.state('');

    const load = async () => {
        try {
            const decoded = await capi.postV1DeploymentsRecentlyDeleted({});
            items.val = (decoded?.items || []).filter(record => record?.deployment);
        } catch (e) {
            error.val = e?.message || 'Loading deleted deployments failed.';
            items.val = [];
        }
    };
    setTimeout(load, 0);

    const headerCell = (label, extraClass = '') => th(
        {class: `px-3 py-2 font-medium ${extraClass}`},
        label,
    );

    const row = (config) => tr(
        {
            class: "border-b border-gray-800 slate:border-line graphite:border-line last:border-0",
            "data-testid": `recently-deleted-row-${config.deployment.id}`,
        },
        td({class: "px-3 py-2 text-gray-200"},
            div(config.deployment.name || `#${config.deployment.id}`),
            () => {
                const source = sourceLabel(config);
                return source ? div({class: "text-xs text-gray-500 truncate"}, source) : '';
            },
        ),
        td({class: "px-3 py-2 text-gray-400"}, spaceName(config.deployment.spaceId)),
        td({class: "px-3 py-2 text-gray-400"}, nodeDisplayName(placementNodeId(config), machinesS.val) || '—'),
        td({class: "px-3 py-2 text-gray-400 whitespace-nowrap"}, formatHistoryTime(new Date(Number(config.meta?.updatedTime || 0))) || '—'),
        td({class: "px-3 py-2 text-gray-400"}, resolveUserDisplayName(Number(config.meta?.updatedActor || 0)) || '—'),
        td({class: "px-3 py-2 text-right"},
            button({
                type: "button",
                class: "dep-btn dep-btn-blue text-xs px-3 py-1",
                onclick: () => onFork(config),
            }, "Fork"),
        ),
    );

    const body = () => {
        if (error.val) return p({class: "px-4 py-6 text-sm text-red-400"}, error.val);
        if (items.val === null) return p({class: "px-4 py-6 text-sm text-gray-400"}, "Loading...");
        if (items.val.length === 0) {
            return p({class: "px-4 py-6 text-sm text-gray-400"}, "No deployments have been deleted.");
        }
        return table(
            {class: "w-full min-w-[46rem] text-sm"},
            thead(
                tr(
                    {class: "dep-dialog-head text-left"},
                    headerCell("Deployment"),
                    headerCell("Space"),
                    headerCell("Node"),
                    headerCell("Deleted"),
                    headerCell("Deleted by"),
                    headerCell("", "text-right"),
                ),
            ),
            tbody(...items.val.map(row)),
        );
    };

    return div(
        div({class: "fixed inset-0 bg-black/60 z-40", onclick: onClose}),
        div(
            {
                class: "fixed inset-0 z-50 flex items-center justify-center p-4 md:p-8 pointer-events-none",
                "data-testid": "recently-deleted-overlay",
                role: "dialog",
                "aria-modal": "true",
                "aria-labelledby": "recently-deleted-title",
            },
            div(
                {
                    class: "dep-dialog pointer-events-auto w-full max-w-4xl max-h-[80vh]",
                    onclick: (e) => e.stopPropagation(),
                },
                div(
                    {class: "flex items-start justify-between gap-4 border-b border-gray-700 slate:border-line graphite:border-line px-4 py-3"},
                    div(
                        h2({id: "recently-deleted-title", class: "text-base font-semibold"}, "Recently deleted"),
                        p({class: "text-xs text-gray-400"},
                            "Forking creates a new deployment from the deleted config. It does not restore the " +
                            "original's history, volumes, or logs."),
                    ),
                    button({
                        type: "button",
                        class: "dep-btn text-xs px-3 py-1",
                        onclick: onClose,
                    }, "Close"),
                ),
                div({class: "min-h-0 flex-1 overflow-auto"}, body),
            ),
        ),
    );
}
