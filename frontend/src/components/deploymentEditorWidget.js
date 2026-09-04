import van from "vanjs-core";
import {spinnerButton} from "./spinnerbutton.js";
import {formInvalidReason} from "./deploymentForm.js";
import {deploymentUiHasOpenPane, deploymentUiWidget} from "./deploymentUiWidget.js";
import {DeploymentCreationUpdate} from "./deploymentCreationUpdate.js";
import {sourceFooterWidgets} from "./deploymentSourceFooter.js";
import {defaultPlacement, placeholderDeploymentName} from "../lib/nodeSpaces.js";

const {div, span, button, p} = van.tags;

// The UI/Code choice is remembered across editors: an editor opens in the
// last mode chosen in any other, as soon as that mode is available to it.
// Code is the default until the user picks UI somewhere.
const EDITOR_MODE_KEY = 'opsagent_deployment_editor_mode';
const loadEditorMode = () => {
    try {
        return localStorage.getItem(EDITOR_MODE_KEY) === 'ui' ? 'ui' : 'code';
    } catch {
        return 'code';
    }
};
const saveEditorMode = mode => {
    try {
        localStorage.setItem(EDITOR_MODE_KEY, mode);
    } catch {}
};

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

// deploymentEditorWidget(opts)
//   mode: 'create' | 'update'
//   layout: 'dialog' (default, a fixed-width card) | 'page' (fills its host)
//   dirty: optional van.state the editor keeps true while the document
//          differs from what it opened with, or Code mode holds an
//          unparsed draft
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
    // Folder trees the HCL resolves reference paths against.
    const valueDirectories = catalogs.valueDirectories || [];
    const assetDirectories = catalogs.assetDirectories || [];
    const nodes = asState(catalogs.nodes, []);
    const nodesLoaded = asState(catalogs.nodesLoaded, true);
    const deploymentRow = opts.deploymentRow || null;
    const deployment = opts.deployment || null;
    const deploymentUpdate = new DeploymentCreationUpdate({
        mode,
        deploymentRow,
        deployment,
        validateSource: actions.validateSource,
        loadDeploymentVersions: actions.loadDeploymentVersions,
    });
    const form = deploymentUpdate.form;
    const editorMode = van.state('ui');

    if (mode === 'create' && opts.fork) {
        // A fork is always a new deployment: it never inherits the source's id,
        // history, volumes, or logs.
        form.deploymentId.val = 0;
        // Forking a live deployment would collide on (node, space, name), so the
        // identity is cleared for the user to choose. A deleted one has already
        // released that tuple, so its identity is kept: recovering a deployment
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

    if (mode === 'create' && !opts.fork) {
        // A blank create starts with a usable identity: a placeholder name
        // and the first visible space/node pair the allow list permits.
        // Without such a pair the form stays on the global space with no
        // node, which the HCL renders as a placeholder node reference.
        if (!form.name.val) form.name.val = placeholderDeploymentName();
        const placement = defaultPlacement(stateValue(spaces) || [], stateValue(nodes) || [], form.spaceId.val);
        if (placement) {
            form.spaceId.val = placement.spaceId;
            form.nodeId.val = placement.nodeId;
        }
    }
    if (mode === 'create' && !form.nodeId.val) {
        // A lone node whose allow list has not arrived yet is still the only
        // possible choice.
        const nodeList = (stateValue(nodes) || []).filter(node => Number(node?.id || 0));
        if (nodeList.length === 1) form.nodeId.val = nodeList[0].id;
    }

    // Dirty tracking starts after the seeded defaults so an untouched editor
    // reads clean.
    const initialDocumentKey = JSON.stringify(deploymentUpdate.toDocument());
    const codeDraftInvalid = van.state(false);
    if (opts.dirty) {
        van.derive(() => {
            opts.dirty.val = codeDraftInvalid.val
                || JSON.stringify(deploymentUpdate.toDocument()) !== initialDocumentKey;
        });
    }

    const documentInvalidReason = () => {
        const sourceReason = deploymentUpdate.sourceInvalidReason();
        if (sourceReason) return sourceReason;
        const reason = formInvalidReason(form, {
            nodeOptions: stateValue(nodes) || [],
            deployments: stateValue(deployments) || [],
        });
        if (reason) return reason;
        return deploymentUpdate.versionInvalidReason()
            || (canEditState ? deploymentUpdate.runningInvalidReason() : '');
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
            const moved = movePayload ? await actions.updateDeployment(movePayload) : null;
            let result = moved;
            if (mode === 'create') {
                result = await actions.createDeployment(payload);
                if (!result?.id) throw new Error('Create response did not include a deployment ID');
            } else if (payload) {
                if (moved) payload.expectedVersion = Number(moved.version || 0) + 1;
                result = await actions.updateDeployment(payload);
            }
            notifySuccess(mode, payload, result);
        } catch (e) {
            errorMsg.val = e.message || (mode === 'create' ? 'Failed to create deployment' : 'Deploy failed');
            throw e;
        }
    });

    // A tinted brand button at the footer toolbar height: the solid brand
    // button read as loud beside the muted footer widgets.
    const submitButton = spinnerButton(
        mode === 'create' ? 'Create' : 'Update deployment',
        doSubmit,
        'h-[30px] border border-blue-400/45 bg-blue-500/15 text-xs text-blue-100 hover:border-blue-300/70 hover:bg-blue-500/30 hover:text-blue-50',
        'button',
        () => Boolean(invalidReason()),
        {base: 'rounded-md px-3 font-medium', disabledClass: 'opacity-45 cursor-not-allowed'},
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
    });
    // Code editing needs the catalogs the HCL resolves names against; the
    // system deployments have no container spec to edit as code.
    const codeAvailable = () => (mode === 'create' || deploymentRow?.runnerType === 'container')
        && stateValue(nodesLoaded) !== false;
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
                catalogs: {spaces, nodes, assets, secretRefs, configRefs, deployments, valueDirectories, assetDirectories},
                versionCompletions: () => deploymentUpdate.versionOptions(),
                constraints: mode === 'update' ? {
                    immutableName: form.name.val,
                    immutableNodeId: form.nodeId.val,
                    updateMode: true,
                } : {},
            });
            van.derive(() => { codeDraftInvalid.val = codeWidget.draftInvalid.val; });
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
    const selectEditorMode = (nextMode, {remember = true} = {}) => {
        if (nextMode === 'code' && !codeAvailable()) return;
        editorMode.val = nextMode;
        if (remember) saveEditorMode(nextMode);
        if (nextMode === 'code') {
            void ensureCodeWidget().then(widget => {
                if (widget && editorMode.val === 'code') requestAnimationFrame(() => widget.activate());
            });
        }
    };
    // Honour a remembered Code preference as soon as code editing is
    // available (catalogs loaded). Applying the preference is not itself a
    // choice, so it is not re-saved.
    let preferCode = loadEditorMode() === 'code';
    van.derive(() => {
        if (preferCode && codeAvailable()) {
            preferCode = false;
            selectEditorMode('code', {remember: false});
        }
    });
    const hasOpenPane = () => editorMode.val === 'ui'
        && deploymentUiHasOpenPane(form);
    const editorHeight = opts.maxHeight || '88vh';
    const modeToggle = editorModeToggle({editorMode, codeAvailable, selectEditorMode});
    // A footer version pick also patches the HCL text in Code mode. In UI
    // mode the form is the source of truth and committing a stale code draft
    // would revert form edits, so only Code mode patches the text.
    const footerWidgets = sourceFooterWidgets({
        deploymentUpdate,
        onSelectVersion: version => {
            if (editorMode.val === 'code' && codeWidget) codeWidget.setWorkloadVersion(version);
        },
    });
    // layout 'page' fills whatever hosts it (a page tab) instead of framing
    // itself as a fixed-width dialog.
    const pageLayout = opts.layout === 'page';

    return div(
        {
            class: pageLayout
                ? 'bg-gray-900 flex h-full w-full flex-col overflow-hidden'
                : 'bg-gray-900 border border-gray-600 rounded-lg shadow-[0_28px_90px_rgba(0,0,0,0.5)] flex flex-col overflow-hidden',
            'data-testid': mode === 'create' ? 'create-deployment-dialog' : 'update-deployment-dialog',
            style: () => pageLayout
                ? ''
                : `width: ${hasOpenPane() ? 1560 : 1120}px; max-width: 100%; height: ${editorHeight}; max-height: ${editorHeight};`,
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
            footerWidgets,
            invalidReason,
            requestDescription,
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
        title: () => mode === 'code' && !args.codeAvailable() ? 'Code editing is unavailable until configuration data is loaded' : '',
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
    return div(
        {class: 'flex shrink-0 items-center justify-between gap-3 bg-gray-950/90 px-4 py-2.5'},
        div(
            {class: 'flex min-w-0 items-center gap-3'},
            args.modeToggle,
            ...args.footerWidgets,
            requestStatus(args.requestDescription),
        ),
        div(
            {class: 'flex min-w-0 items-center justify-end gap-3'},
            () => args.invalidReason()
                ? p({
                    class: 'truncate text-xs text-amber-300',
                    'data-testid': args.mode === 'create' ? 'create-validation-reason' : 'update-validation-reason',
                }, args.invalidReason())
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
        {class: () => requestDescription.val ? 'inline-flex items-center gap-2 text-xs text-gray-400' : 'hidden'},
        span({class: 'w-[1.1em] h-[1.1em] border-[0.15em] border-gray-500/30 border-t-gray-300 rounded-full animate-spin'}),
        span(() => requestDescription.val || 'Idle'),
    );
}
