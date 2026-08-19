import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {loadAssetPreview} from "../lib/assetContent.js";
import {
    assetMetasS,
    deploymentsS,
    nodesS,
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
        retainIdentity: Boolean(opts.retainIdentity),
        catalogs: {
            spaces: spacesS,
            nodes: nodesS,
            nodesLoaded: true,
            deployments: deploymentsS,
            assets: assetMetasS,
            secretRefs: secretRefsS,
            configRefs: userConfigRefsS,
        },
        actions: {
            validateSource: request => capi.postV1ReposValidate(request),
            loadAsset: loadAssetPreview,
            createAsset: request => capi.postV1AssetsCreate(request),
            saveVersion: request => capi.postV1AssetsSet(request),
            createDeployment: request => capi.postV1DeploymentsCreate(request),
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
