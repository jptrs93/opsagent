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

export function deployOverlay(deployment, deploymentConfig, onClose, onDeployed) {
    const editor = deploymentEditorWidget({
        mode: 'update',
        deployment,
        deploymentConfig,
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
            loadDeploymentVersions: request => capi.postV1DeploymentsVersions(request),
            loadAsset: loadAssetPreview,
            createAsset: request => capi.postV1AssetsCreate(request),
            saveVersion: request => capi.postV1AssetsSet(request),
            updateDeployment: request => capi.postV1DeploymentsUpdate(request),
            moveDeploymentSpace: request => capi.postV1DeploymentsMoveSpace(request),
        },
        onCancel: onClose,
        onSuccess: () => {
            if (onDeployed) onDeployed();
            onClose();
        },
    });

    return div(
        div({class: 'fixed inset-0 bg-black/60 z-40', onclick: onClose}),
        div(
            {class: 'fixed inset-0 z-50 flex items-center justify-center p-4 md:p-8 pointer-events-none'},
            div({class: 'pointer-events-auto max-w-full', onclick: event => event.stopPropagation()}, editor),
        ),
    );
}
