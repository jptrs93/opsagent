import van from "vanjs-core";
import {spinnerButton} from "./spinnerbutton.js";
import {formInvalidReason} from "./deploymentForm.js";
import {deploymentUiHasOpenPane, deploymentUiWidget} from "./deploymentUiWidget.js";
import {DeploymentCreationUpdate, SOURCE_DOCKER_IMAGE, SOURCE_NIX_DOCKER} from "./deploymentCreationUpdate.js";
import {imageVersionFromReference} from "./deploymentSource.js";

const {div, span, button, p} = van.tags;

const stateValue = (value) => value && typeof value === 'object' && 'val' in value ? value.val : value;
const asState = (value, fallback) => value && typeof value === 'object' && 'val' in value
    ? value
    : van.state(value ?? fallback);
let deploymentCodeWidgetLoader;

const loadDeploymentCodeWidget = () => {
    deploymentCodeWidgetLoader ||= import('./deploymentCodeWidget.js')
        .then(module => module.deploymentCodeWidget);
    return deploymentCodeWidgetLoader;
};

export function preloadDeploymentCodeWidget() {
    void loadDeploymentCodeWidget().catch(() => {});
}

export function deploymentEditorWidget(opts) {
    const mode = opts.mode;
    if (mode !== 'create' && mode !== 'update') throw new Error(`Unsupported deployment editor mode: ${mode}`);

    const actions = opts.actions || {};
    if (typeof actions.validateSource !== 'function') throw new Error('actions.validateSource is required');
    if (mode === 'create' && typeof actions.createDeployment !== 'function') throw new Error('actions.createDeployment is required');
    if (mode === 'update' && typeof actions.updateDeployment !== 'function') throw new Error('actions.updateDeployment is required');

    const catalogs = opts.catalogs || {};
    const spaces = catalogs.spaces || [];
    const deployments = catalogs.deployments || [];
    const assets = catalogs.assets || [];
    const secretRefs = catalogs.secretRefs || [];
    const configRefs = catalogs.configRefs || [];
    const nodes = asState(catalogs.nodes, []);
    const nodesLoaded = asState(catalogs.nodesLoaded, true);
    const deploymentRow = opts.deploymentRow || null;
    const deployment = opts.deployment || null;
    const deploymentUpdate = new DeploymentCreationUpdate({
        mode,
        deploymentRow,
        deployment,
        validateSource: actions.validateSource,
    });
    const form = deploymentUpdate.form;
    const editorMode = van.state('ui');

    if (mode === 'create' && opts.fork) {
        // A fork is always a new deployment: it never inherits the source's id,
        // history, volumes, or logs.
        form.deploymentId.val = 0;
        // Forking a live deployment would collide on (node, space, name), so the
        // identity is cleared for the user to choose. A deleted one has already
        // released that tuple, so its identity is kept — recovering a deployment
        // under its old name is the whole point of forking one.
        if (!opts.retainIdentity) {
            form.name.val = '';
            form.spaceId.val = 1;
            form.nodeId.val = 0;
        }
    }

    const canEditState = mode === 'create' || deploymentRow?.runnerType !== 'opendeploy';
    const requestDescription = van.state('');
    const errorMsg = van.state('');
    let requestSeq = 0;

    const startRequest = (description) => {
        const seq = ++requestSeq;
        requestDescription.val = description;
        return () => {
            if (requestSeq === seq) requestDescription.val = '';
        };
    };

    const withRequest = async (description, action) => {
        const endRequest = startRequest(description);
        try {
            return await action();
        } finally {
            endRequest();
        }
    };

    const notifySuccess = (kind, payload, result) => {
        if (opts.onSuccess) opts.onSuccess({kind, payload, result, form, deploymentUpdate});
    };

    if (mode === 'create' && !form.nodeId.val) {
        const nodeList = (stateValue(nodes) || []).filter(node => Number(node?.id || 0));
        if (nodeList.length === 1) form.nodeId.val = nodeList[0].id;
    }

    const loadVersions = async (branch, loadOpts = {}) => {
        return deploymentUpdate.loadVersions({
            branch,
            preserveSelection: loadOpts.preserveSelection,
            refreshAvailableBranches: loadOpts.refreshAvailableBranches,
        });
    };

    if (mode === 'create' && deploymentUpdate.desiredRunning.val && form.sourceType.val === SOURCE_NIX_DOCKER) {
        void deploymentUpdate.validateExactNixSelection();
    }

    if (mode === 'update' && deploymentRow?.variant
        && (deploymentRow.variant === SOURCE_NIX_DOCKER
            || (deploymentRow.variant === SOURCE_DOCKER_IMAGE && !imageVersionFromReference(form.containerImage.val)))) {
        void deploymentUpdate.loadExistingDeploymentVersions(
            actions.loadDeploymentVersions,
            deploymentRow.id,
            {preserveSelection: true},
        );
    }

    const documentInvalidReason = () => {
        const sourcePathReason = deploymentUpdate.sourcePathInvalidReason();
        if (sourcePathReason) return sourcePathReason;
        const reason = formInvalidReason(form, {
            nodeOptions: stateValue(nodes) || [],
            deployments: stateValue(deployments) || [],
        });
        if (reason) return reason;
        const runningNixReason = deploymentUpdate.runningNixInvalidReason();
        if (runningNixReason) return runningNixReason;
        if (canEditState && deploymentUpdate.desiredRunning.val
            && form.sourceType.val !== SOURCE_NIX_DOCKER
            && !deploymentUpdate.createDesiredVersion()) {
            return 'Select a version before setting the deployment to Running.';
        }
        return '';
    };

    let codeWidget = null;
    const codeEditorStatus = van.state('idle');
    const codeEditorError = van.state('');
    const invalidReason = () => editorMode.val === 'code'
        ? (codeEditorError.val || (codeEditorStatus.val === 'loading' ? 'Loading code editor.' : '') || codeWidget?.invalidReason() || documentInvalidReason())
        : documentInvalidReason();

    const doSubmit = async () => withRequest(mode === 'create' ? 'Creating deployment.' : 'Updating deployment.', async () => {
        errorMsg.val = '';
        const reason = invalidReason();
        if (reason) {
            errorMsg.val = reason;
            throw new Error(reason);
        }
        const payload = mode === 'create'
            ? deploymentUpdate.toCreatePayload()
            : deploymentUpdate.toUpdatePayload();
        const movePayload = mode === 'create' ? null : deploymentUpdate.toMovePayload();
        try {
            // Move first so spec validation runs against the destination space.
            if (movePayload) await actions.moveDeploymentSpace(movePayload);
            const result = mode === 'create'
                ? await actions.createDeployment(payload)
                : await actions.updateDeployment(payload);
            if (mode === 'create' && !result?.id) throw new Error('Create response did not include a deployment ID');
            notifySuccess(mode, payload, result);
        } catch (e) {
            errorMsg.val = e.message || (mode === 'create' ? 'Failed to create deployment' : 'Deploy failed');
            throw e;
        }
    });

    const submitButton = spinnerButton(
        mode === 'create' ? 'Create' : 'Update deployment',
        doSubmit,
        'btn-primary text-sm py-1.5 px-4',
        'button',
        () => Boolean(invalidReason()),
    );
    if (mode === 'create') submitButton.dataset.testid = 'create-deployment-submit';

    const uiWidget = deploymentUiWidget({
        mode,
        form,
        deploymentRow,
        deploymentUpdate,
        canEditState,
        spaces,
        nodes,
        nodesLoaded,
        assets,
        secretRefs,
        configRefs,
        deployments,
        loadAsset: actions.loadAsset,
        createAsset: request => withRequest('Saving asset.', async () => {
            if (typeof actions.createAsset !== 'function') throw new Error('actions.createAsset is required');
            return actions.createAsset(request);
        }),
        saveVersion: request => withRequest('Saving asset.', async () => {
            if (typeof actions.saveVersion !== 'function') throw new Error('actions.saveVersion is required');
            return actions.saveVersion(request);
        }),
        onRefresh: () => loadVersions(deploymentUpdate.nixDockerBuild.selectedBranch.val, {
            refreshAvailableBranches: true,
            preserveSelection: true,
        }),
    });
    const codeAvailable = () => (mode === 'create' || deploymentRow?.runnerType === 'container')
        && stateValue(nodesLoaded) !== false
        && !requestDescription.val
        && !deploymentUpdate.versionRequestDescription.val;
    const codeHost = div(
        {class: 'flex h-full min-h-0 min-w-0 flex-1 items-center justify-center bg-gray-950'},
        p({class: 'text-xs text-gray-500'}, 'Loading code editor...'),
    );
    const ensureCodeWidget = async () => {
        if (codeWidget) return codeWidget;
        if (codeEditorStatus.val === 'loading') return null;
        codeEditorStatus.val = 'loading';
        codeEditorError.val = '';
        try {
            const deploymentCodeWidget = await loadDeploymentCodeWidget();
            codeWidget = deploymentCodeWidget({
                document: deploymentUpdate.document,
                catalogs: {spaces, nodes, assets, secretRefs, configRefs, deployments},
                constraints: mode === 'update' ? {
                    immutableName: form.name.val,
                    immutableNodeId: form.nodeId.val,
                    updateMode: true,
                    initialVersion: deploymentUpdate.createDesiredVersion(),
                } : {},
            });
            codeHost.replaceChildren(codeWidget.element);
            codeEditorStatus.val = 'ready';
            return codeWidget;
        } catch (error) {
            codeEditorStatus.val = 'error';
            codeEditorError.val = error.message || 'Failed to load code editor.';
            codeHost.replaceChildren(p({class: 'text-xs text-red-400'}, codeEditorError.val));
            return null;
        }
    };
    const selectEditorMode = nextMode => {
        if (nextMode === 'code' && !codeAvailable()) return;
        editorMode.val = nextMode;
        if (nextMode === 'code') {
            deploymentUpdate.cancelSourceRequests();
            requestDescription.val = '';
            void ensureCodeWidget().then(widget => {
                if (widget && editorMode.val === 'code') requestAnimationFrame(() => widget.activate());
            });
        }
    };
    const hasOpenPane = () => editorMode.val === 'ui'
        && deploymentUiHasOpenPane(form);
    const editorHeight = opts.maxHeight || '88vh';
    const modeToggle = editorModeToggle({editorMode, codeAvailable, selectEditorMode});

    return div(
        {
            class: 'bg-gray-900 border border-gray-600 rounded-lg shadow-[0_28px_90px_rgba(0,0,0,0.5)] flex flex-col overflow-hidden',
            'data-testid': mode === 'create' ? 'create-deployment-dialog' : 'update-deployment-dialog',
            style: () => `width: ${hasOpenPane() ? 1560 : 1120}px; max-width: 100%; height: ${editorHeight}; max-height: ${editorHeight};`,
        },
        div(
            {class: 'flex-1 min-h-0 min-w-0'},
            div(
                {class: () => editorMode.val === 'ui' ? 'flex h-full min-h-0 min-w-0' : 'hidden'},
                uiWidget,
            ),
            div(
                {class: () => editorMode.val === 'code' ? 'flex h-full min-h-0 min-w-0' : 'hidden'},
                codeHost,
            ),
        ),
        () => errorMsg.val
            ? div({class: 'bg-gray-950/80 px-4 pt-2'}, p({class: 'text-xs text-red-400'}, errorMsg.val))
            : '',
        editorFooter({
            mode,
            modeToggle,
            invalidReason,
            requestDescription: van.derive(() => requestDescription.val || deploymentUpdate.versionRequestDescription.val),
            submitButton,
            onCancel: opts.onCancel,
        }),
    );
}

function editorModeToggle(args) {
    const modeButton = (label, mode) => button({
        type: 'button',
        role: 'tab',
        'data-testid': `deployment-editor-mode-${mode}`,
        'aria-selected': () => String(args.editorMode.val === mode),
        disabled: () => mode === 'code' && !args.codeAvailable(),
        title: () => mode === 'code' && !args.codeAvailable() ? 'Code editing is unavailable until configuration data is loaded and current requests finish' : '',
        class: () => args.editorMode.val === mode
            ? 'rounded-md bg-gray-700 px-2.5 py-1 text-[11px] font-medium text-gray-100 shadow-sm cursor-pointer'
            : 'rounded-md px-2.5 py-1 text-[11px] font-medium text-gray-500 hover:bg-gray-800 hover:text-gray-200 cursor-pointer disabled:opacity-40 disabled:cursor-not-allowed',
        onclick: () => args.selectEditorMode(mode),
    }, label);

    return div(
        {class: 'flex shrink-0 rounded-lg bg-black/20 p-0.5', role: 'tablist', 'aria-label': 'Configuration editor mode'},
        modeButton('UI', 'ui'),
        modeButton('Code', 'code'),
    );
}

function editorFooter(args) {
    if (args.mode === 'create') {
        return div(
            {class: 'flex shrink-0 items-center justify-between gap-4 bg-gray-950/90 px-4 py-2.5'},
            args.modeToggle,
            div(
                {class: 'flex min-w-0 items-center justify-end gap-3'},
                () => args.invalidReason()
                    ? p({class: 'truncate text-xs text-amber-300', 'data-testid': 'create-validation-reason'}, args.invalidReason())
                    : '',
                cancelButton(args.onCancel),
                args.submitButton,
            ),
        );
    }

    return div(
        {class: 'flex shrink-0 items-center justify-between gap-3 bg-gray-950/90 px-4 py-2.5'},
        div(
            {class: 'flex min-w-0 items-center gap-3'},
            args.modeToggle,
            requestStatus(args.requestDescription),
        ),
        div(
            {class: 'flex min-w-0 items-center justify-end gap-3'},
            () => args.invalidReason()
                ? p({class: 'truncate text-xs text-amber-400'}, args.invalidReason())
                : '',
            cancelButton(args.onCancel),
            args.submitButton,
        ),
    );
}

function cancelButton(onCancel) {
    return button({
        class: 'text-sm text-gray-400 hover:text-gray-200 cursor-pointer px-3 py-1.5',
        onclick: onCancel,
        type: 'button',
    }, 'Cancel');
}

function requestStatus(requestDescription) {
    return span(
        {class: () => requestDescription.val ? 'inline-flex items-center gap-2 text-xs text-gray-400' : 'invisible text-xs'},
        span({class: 'w-[1.1em] h-[1.1em] border-[0.15em] border-gray-500/30 border-t-gray-300 rounded-full animate-spin'}),
        span(() => requestDescription.val || 'Idle'),
    );
}
