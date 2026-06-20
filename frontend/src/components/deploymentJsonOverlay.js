import van from "vanjs-core";
import {deploymentsS} from "../state/deployments.js";

const {div, h2, p, pre, button} = van.tags;

function currentDeploymentConfig(deploymentId) {
    return (deploymentsS.val || []).find(item => item.config?.id === deploymentId)?.config || null;
}

function deploymentConfigJson(deploymentId) {
    const config = currentDeploymentConfig(deploymentId);
    return JSON.stringify(config || {error: "deployment config not found", deploymentId}, null, 2);
}

export function deploymentJsonOverlay(deploymentId, deploymentLabel, onClose) {
    const copied = van.state(false);

    const copyJson = async () => {
        await navigator.clipboard.writeText(deploymentConfigJson(deploymentId));
        copied.val = true;
        setTimeout(() => { copied.val = false; }, 1500);
    };

    return div(
        div({class: "fixed inset-0 bg-black/70 z-40", onclick: onClose}),
        div(
            {class: "fixed inset-0 z-50 flex items-center justify-center p-3 md:p-6 pointer-events-none", "data-testid": "deployment-json-overlay"},
            div(
                {class: "w-full h-full max-w-[1200px] max-h-[90vh] bg-gray-900 border border-gray-700 rounded-xl shadow-2xl flex flex-col overflow-hidden pointer-events-auto", onclick: (e) => e.stopPropagation()},
                div(
                    {class: "flex items-center justify-between gap-4 px-4 py-3 border-b border-gray-700"},
                    div({class: "min-w-0"},
                        h2({class: "text-sm font-semibold text-gray-200 truncate"}, `Deployment JSON: ${deploymentLabel}`),
                        p({class: "text-xs text-gray-500"}, "Current deployment config from the live state stream."),
                    ),
                    button({class: "text-sm text-gray-400 hover:text-gray-200 cursor-pointer px-3 py-1.5", onclick: onClose}, "Close"),
                ),
                div({class: "flex-1 min-h-0 bg-gray-950 overflow-hidden"},
                    pre(
                        {class: "h-full w-full overflow-auto p-4 text-xs font-mono whitespace-pre-wrap break-all leading-5 text-gray-200"},
                        () => deploymentConfigJson(deploymentId),
                    ),
                ),
                div(
                    {class: "flex items-center justify-end gap-2 px-4 py-3 border-t border-gray-700"},
                    button({class: "btn-secondary text-sm py-1.5 px-3 cursor-pointer", onclick: copyJson}, () => copied.val ? "Copied" : "Copy"),
                ),
            ),
        ),
    );
}
