// Confirmation for a forced restart: the deployment keeps its version and
// config and the scheduler replaces the placement under its upgrade strategy.
// Shared by the status page inspector and the deployment editor footer.

import van from "vanjs-core";
import {containerWorkload} from "../lib/deployment.js";

const {button, div, h2, p} = van.tags;

export const formatDeploymentLabel = (deploymentRow) => {
    if (!deploymentRow) return 'unknown deployment';
    const parts = [deploymentRow.spaceName, deploymentRow.node, deploymentRow.name].filter(Boolean);
    return parts.length > 0 ? parts.join(' / ') : `#${deploymentRow.id}`;
};

// restartDeploymentPayload is the DeploymentUpdateRequestV2 for a restart at
// the version the caller last saw.
export function restartDeploymentPayload(deploymentRow) {
    return {
        deploymentId: deploymentRow.id,
        expectedVersion: (deploymentRow.version || 0) + 1,
        restartUpdate: {},
    };
}

// restartDeploymentOverlay({deploymentRow, rawConfig, restart, close})
//   restart: async () => sends the request; the overlay closes when it
//            resolves and shows the error when it rejects
export function restartDeploymentOverlay({deploymentRow, rawConfig, restart, close}) {
    const saving = van.state(false);
    const error = van.state('');
    const label = formatDeploymentLabel(deploymentRow);
    const rollover = Number(containerWorkload(rawConfig)?.upgradeStrategy || 0) === 2;
    const effect = rollover
        ? 'A replacement starts alongside the current container and takes over once it signals readiness.'
        : 'The current container stops and a replacement starts. The workload is unavailable in between.';

    const confirmRestart = async () => {
        if (saving.val) return;
        error.val = '';
        saving.val = true;
        try {
            await restart();
            close();
        } catch (e) {
            error.val = e?.message || 'Restarting deployment failed.';
        } finally {
            saving.val = false;
        }
    };

    return div(
        {class: "fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4", "data-testid": "deployment-restart-overlay"},
        div(
            {class: "card w-full max-w-md flex flex-col gap-4 shadow-2xl"},
            h2({class: "text-base font-semibold"}, "Restart deployment"),
            p({class: "text-sm text-gray-300"}, `Restart ${label} at its current version and config? ${effect}`),
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
                    class: "text-xs px-3 py-1 rounded-md font-medium bg-brand text-white hover:bg-blue-600 disabled:opacity-60 cursor-pointer",
                    disabled: () => saving.val,
                    "data-testid": "deployment-restart-confirm",
                    onclick: confirmRestart,
                }, () => saving.val ? "Restarting..." : "Restart"),
            ),
        ),
    );
}
