import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {
    assetMetasS,
    deploymentsS,
    secretRefsS,
    spacesS,
    userConfigRefsS,
} from "../state/deployments.js";
import {deploymentEditorWidget} from "./deploymentEditorWidget.js";

const {div} = van.tags;

export function createOverlay(onClose, onCreated, opts = {}) {
    const editor = deploymentEditorWidget({
        mode: 'create',
        deployment: opts.sourceDeployment || null,
        deploymentConfig: opts.sourceDeploymentConfig || null,
        fork: Boolean(opts.sourceDeploymentConfig),
        catalogs: {
            spaces: spacesS,
            deployments: deploymentsS,
            assets: assetMetasS,
            secretRefs: secretRefsS,
            configRefs: userConfigRefsS,
        },
        actions: {
            loadNodes: () => capi.getV1ClusterStatus(),
            validateSource: request => capi.postV1RepoValidate(request),
            saveAsset: request => capi.postV1AssetsSet(request),
            createDeployment: request => capi.postV1DeploymentCreate(request),
        },
        onCancel: onClose,
        onSuccess: () => {
            if (onCreated) onCreated();
            onClose();
        },
    });

    return overlay(editor, onClose);
}

function overlay(editor, onClose) {
    return div(
        div({class: 'fixed inset-0 bg-black/60 z-40', onclick: onClose}),
        div(
            {class: 'fixed inset-0 z-50 flex items-center justify-center p-4 md:p-8 pointer-events-none'},
            div({class: 'pointer-events-auto max-w-full', onclick: event => event.stopPropagation()}, editor),
        ),
    );
}
