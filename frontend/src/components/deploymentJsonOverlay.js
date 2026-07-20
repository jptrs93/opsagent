import van from "vanjs-core";
import {deploymentsS} from "../state/deployments.js";
import {cleanDeploymentConfig, deploymentConfigToYaml, orderDeploymentConfig} from "../yaml/deploymentConfig.js";

const {div, span, pre, button} = van.tags;

function currentDeploymentConfig(deploymentId) {
    return (deploymentsS.val || []).find(item => item.config?.id === deploymentId)?.config || null;
}

function deploymentConfigJson(deploymentId) {
    const config = currentDeploymentConfig(deploymentId);
    const cleaned = cleanDeploymentConfig(config);
    return JSON.stringify(cleaned ? orderDeploymentConfig(cleaned) : {error: "deployment config not found", deploymentId}, null, 2);
}

function deploymentConfigYaml(deploymentId) {
    const config = currentDeploymentConfig(deploymentId);
    return deploymentConfigToYaml(config) || deploymentConfigToYaml({error: "deployment config not found", deploymentId});
}

function titleField(label, value) {
    return span(
        {class: "inline-flex items-baseline gap-1 whitespace-nowrap"},
        span({class: "text-gray-500"}, `${label}:`),
        span({class: "text-gray-200"}, value || '-'),
    );
}

function modeButton(label, mode, selectedMode) {
    return button({
        class: () => selectedMode.val === mode
            ? "rounded-md bg-gray-200 px-2.5 py-1 text-xs font-medium text-gray-950 cursor-pointer"
            : "rounded-md px-2.5 py-1 text-xs font-medium text-gray-400 hover:bg-gray-800 hover:text-gray-200 cursor-pointer",
        onclick: () => { selectedMode.val = mode; },
        type: "button",
    }, label);
}

export function deploymentConfigOverlay(deployment, onClose) {
    const copied = van.state(false);
    const selectedMode = van.state('yaml');
    const deploymentId = deployment.id;

    const outputText = () => selectedMode.val === 'yaml'
        ? deploymentConfigYaml(deploymentId)
        : deploymentConfigJson(deploymentId);

    const copyConfig = async () => {
        await navigator.clipboard.writeText(outputText());
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
                    {class: "flex flex-col gap-3 px-4 py-3 border-b border-gray-700 md:flex-row md:items-start md:justify-between"},
                    div({class: "min-w-0"},
                        div(
                            {class: "flex flex-wrap gap-x-3 gap-y-1 text-xs"},
                            titleField("space", deployment.spaceName),
                            titleField("name", deployment.name),
                            titleField("node", deployment.node),
                        ),
                    ),
                    div({class: "flex shrink-0 items-center gap-2"},
                        div(
                            {class: "flex rounded-lg border border-gray-700 bg-gray-950 p-0.5"},
                            modeButton("JSON", "json", selectedMode),
                            modeButton("YAML", "yaml", selectedMode),
                        ),
                        button({class: "text-sm text-gray-400 hover:text-gray-200 cursor-pointer px-3 py-1.5", onclick: onClose, type: "button"}, "Close"),
                    ),
                ),
                div({class: "flex-1 min-h-0 bg-gray-950 overflow-hidden"},
                    pre(
                        {class: "app-scroll h-full w-full overflow-auto p-4 text-xs font-mono whitespace-pre-wrap break-all leading-5 text-gray-200"},
                        outputText,
                    ),
                ),
                div(
                    {class: "flex items-center justify-end gap-2 px-4 py-3 border-t border-gray-700"},
                    button({class: "btn-secondary text-sm py-1.5 px-3 cursor-pointer", onclick: copyConfig}, () => copied.val ? "Copied" : "Copy"),
                ),
            ),
        ),
    );
}
