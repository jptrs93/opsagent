import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {deploymentsS} from "../state/deployments.js";
import {spinnerButton} from "./spinnerbutton.js";
import {deploymentForm, emptyDeploymentForm, envVarsPane, formToYaml, isFormValid} from "./deploymentForm.js";

const { div, span, button, p } = van.tags;

export function createOverlay(onClose, onCreated) {
    const errorMsg = van.state('');
    const form = emptyDeploymentForm();
    const machines = van.state([]);
    const machinesLoaded = van.state(false);

    const loadMachines = async () => {
        try {
            const res = await capi.getV1ClusterStatus();
            machines.val = (res.machines || []).map(m => m.name).filter(Boolean).sort();
            if (!form.machine.val && machines.val.length === 1) {
                form.machine.val = machines.val[0];
            }
        } catch (e) {
            errorMsg.val = e.message || 'Failed to load cluster machines';
            machines.val = [];
        }
        machinesLoaded.val = true;
    };

    loadMachines();

    const environmentOptions = () => {
        const envs = new Set();
        for (const d of deploymentsS.val || []) {
            const env = d.config?.configId?.environment;
            if (env) envs.add(env);
        }
        return [...envs].sort();
    };

    const doCreate = async () => {
        errorMsg.val = '';
        if (!isFormValid(form, {machineOptions: machines.val})) {
            errorMsg.val = 'Name, machine, binary source, and required execution fields must be set.';
            throw new Error(errorMsg.val);
        }

        try {
            await capi.postV1DeploymentCreate({yamlContent: formToYaml(form)});
        } catch (e) {
            errorMsg.val = e.message || 'Failed to create deployment';
            throw e;
        }
        if (onCreated) onCreated();
        onClose();
    };

    const backdrop = div({
        class: "fixed inset-0 bg-black/60 z-40",
        onclick: onClose,
    });

    const dialog = div(
        {class: "fixed inset-0 z-50 flex items-center justify-center p-4 md:p-8 pointer-events-none"},
        div(
            {class: "bg-gray-900 border border-gray-700 rounded-xl shadow-2xl flex flex-row overflow-hidden pointer-events-auto",
             style: () => `width: ${form.envPaneOpen.val ? 1240 : 760}px; max-width: calc(100vw - 2rem); max-height: 88vh;`,
             onclick: (e) => e.stopPropagation()},
            div(
                {class: "flex-1 min-w-0 flex flex-col"},
                div(
                    {class: "flex-1 min-h-0 overflow-auto p-4"},
                    () => deploymentForm(form, {
                        environmentOptions: environmentOptions(),
                        machineOptions: machines.val,
                        machineOptionsLoaded: machinesLoaded.val,
                    }),
                ),
                () => {
                    if (!errorMsg.val) return span();
                    return div(
                        {class: "px-4 pb-2"},
                        p({class: "text-xs text-red-400"}, errorMsg.val),
                    );
                },
                div(
                    {class: "flex items-center justify-end gap-3 px-4 py-3 border-t border-gray-700"},
                    button({
                        class: "text-sm text-gray-400 hover:text-gray-200 cursor-pointer px-3 py-1.5",
                        onclick: onClose,
                    }, "Cancel"),
                    spinnerButton("Create", doCreate, "btn-primary text-sm py-1.5 px-4", "button", () => !isFormValid(form, {machineOptions: machines.val})),
                ),
            ),
            envVarsPane(form),
        ),
    );

    return div(backdrop, dialog);
}
