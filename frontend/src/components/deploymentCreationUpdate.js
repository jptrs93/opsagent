import van from "vanjs-core";
import {
    deploymentToForm,
    emptyDeploymentForm,
    formToDeploymentIdentity,
    formToSpec,
    replaceDeploymentFormFromConfig,
} from "./deploymentForm.js";
import {
    attestExactNixValidationResponse,
    attestImageDiscoveryResponse,
    attestNixCommitDiscoveryResponse,
    attestNixRepositoryResponse,
    buildExactNixValidationRequest,
    buildImageDiscoveryRequest,
    buildNixCommitDiscoveryRequest,
    buildNixListingRequest,
    buildNixRepositoryDiscoveryRequest,
    FULL_GIT_COMMIT_RE,
    imageVersionFromReference,
    SOURCE_DOCKER_IMAGE,
    SOURCE_NIX_DOCKER,
    validateLocalFlakePath,
} from "./deploymentSource.js";

export {SOURCE_DOCKER_IMAGE, SOURCE_NIX_DOCKER} from "./deploymentSource.js";

// Source validation is layered and human-triggered. Each layer of the source
// tuple (type, repository, flake path, flake target, image) carries a status:
//
//   trusted      unchanged from the saved deployment; nothing to check
//   unvalidated  not checked since the tuple last changed
//   checking     a request is in flight
//   ok           checked and accepted
//   error        rejected, locally or remotely
//
// The overall state is the union of the layers. Layers drop back to
// unvalidated whenever the tuple changes, and only validate() and the version
// actions below issue requests; typing never does. Version lists belong to the
// tuple they were listed for and are dropped with it.

export const LAYER_NAMES = {repo: "Repository", flake: "Flake path", target: "Flake target", image: "Image"};

const layer = (status, message) => ({status, message});
const emptyVersions = () => ({loaded: false, loading: false, error: ''});
const shortID = id => (id && id.length > 7 && /^[0-9a-f]+$/i.test(id) ? id.slice(0, 7) : id || '');

const sourceTupleOf = form => ({
    type: form.sourceType.val,
    repo: form.nixRepo.val.trim(),
    flake: form.nixFlake.val.trim(),
    target: form.nixTarget.val.trim(),
    image: form.containerImage.val.trim(),
});
const keyOf = tuple => JSON.stringify(tuple);

// Local rules are known instantly; remote existence needs validate(). A
// missing required field reads as unvalidated rather than invalid: a blank
// form has nothing wrong with it yet.
export function localSourceLayers(tuple) {
    if (tuple.type === SOURCE_DOCKER_IMAGE) {
        return {image: tuple.image ? layer('unvalidated', 'Not checked yet.') : layer('unvalidated', 'Image is required.')};
    }
    const flakeLocal = validateLocalFlakePath(tuple.flake);
    return {
        repo: tuple.repo ? layer('unvalidated', 'Not checked yet.') : layer('unvalidated', 'Repository is required.'),
        flake: !tuple.flake
            ? layer('unvalidated', 'Flake path is required.')
            : (flakeLocal.ok
                ? layer('unvalidated', 'Path rules ok. Existence is checked at the selected commit.')
                : layer('error', flakeLocal.message)),
        target: !tuple.target
            ? layer('ok', 'Default flake output.')
            : (tuple.target.startsWith('.#')
                ? layer('ok', 'Local selector. The output itself is only known at build time.')
                : layer('error', 'Flake target must be a local selector starting with .#.')),
    };
}

export function overallSourceStatus(layers) {
    const states = Object.values(layers || {}).map(item => item.status);
    if (states.includes('error')) return 'error';
    if (states.includes('checking')) return 'checking';
    if (states.length && states.every(status => status === 'trusted')) return 'trusted';
    if (states.length && states.every(status => status === 'trusted' || status === 'ok')) return 'ok';
    return 'unvalidated';
}

export class DeploymentCreationUpdate {
    constructor({mode = null, deploymentRow = null, deployment = null, validateSource, loadDeploymentVersions = null} = {}) {
        if (typeof validateSource !== 'function') throw new Error('validateSource is required');
        const editorMode = mode || (deploymentRow ? 'update' : 'create');
        if (editorMode !== 'create' && editorMode !== 'update') throw new Error(`Unsupported deployment editor mode: ${editorMode}`);
        this.validateSource = validateSource;
        this.loadDeploymentVersions = typeof loadDeploymentVersions === 'function' ? loadDeploymentVersions : null;
        this.mode = editorMode;
        this.existingState = deploymentRow;
        this.form = deployment ? deploymentToForm(deployment) : emptyDeploymentForm();
        const workload = deployment?.def?.spec?.container1Spec || deployment?.def?.spec?.opendeploySpec;
        const initialRunning = editorMode === 'create'
            ? (deploymentRow ? Boolean(deploymentRow.desiredRunning) : true)
            : (deployment?.def?.spec?.opendeploySpec ? true : (workload ? Boolean(workload.running) : Boolean(deploymentRow?.desiredRunning)));
        this.desiredRunning = van.state(initialRunning);
        this.documentRevision = van.state(0);
        this.initialSpecKey = JSON.stringify(formToSpec(this.form));
        this.initialSource = sourceTupleOf(this.form);

        const configuredVersion = workload?.version || deploymentRow?.deployedVersion || '';
        const deployedNixVersion = deploymentRow?.variant === SOURCE_NIX_DOCKER ? configuredVersion : '';
        const explicitImageVersion = imageVersionFromReference(this.form.containerImage.val);
        const deployedImageVersion = deploymentRow?.variant === SOURCE_DOCKER_IMAGE ? configuredVersion : '';
        this.nixDockerBuild = {
            selectedBranch: van.state(''),
            selectedCommit: van.state(deployedNixVersion),
            branches: van.state([]),
            commits: van.state([]),
        };
        this.containerImage = {
            selectedTag: van.state(explicitImageVersion || deployedImageVersion),
            tags: van.state([]),
        };
        this.layers = van.state({});
        this.versions = van.state(emptyVersions());
        this.persistedSourceKey = editorMode === 'update' ? keyOf(this.initialSource) : null;
        this.sourceKey = null;
        this.sourceTuple = null;
        this.requestSequences = {list: 0, exact: 0};
        this.document = {
            read: () => this.toDocument(),
            replace: document => this.replaceDocument(document),
            revision: this.documentRevision,
        };
        this.syncSourceTuple();
        van.derive(() => this.syncSourceTuple());
    }

    // ---- source tuple and layers -------------------------------------------

    currentSourceTuple() {
        return sourceTupleOf(this.form);
    }

    // syncSourceTuple resets the layers when the tuple changes. It runs on
    // every form edit (through the derive, which is asynchronous), at the
    // start of every action so requests key on the tuple as it is now, and
    // from replaceDocument, so the later derive pass sees no further change.
    syncSourceTuple() {
        const tuple = this.currentSourceTuple();
        const key = keyOf(tuple);
        if (key === this.sourceKey) return;
        const previous = this.sourceTuple;
        this.sourceKey = key;
        this.sourceTuple = tuple;
        this.requestSequences.list += 1;
        this.requestSequences.exact += 1;
        if (previous) {
            // Lists were fetched for the old tuple. A different repository,
            // image, or source type also invalidates the selection; a flake
            // or target edit keeps it and re-checks it on the next validate.
            this.versions.val = emptyVersions();
            this.nixDockerBuild.branches.val = [];
            this.nixDockerBuild.commits.val = [];
            this.containerImage.tags.val = [];
            const identityChanged = previous.type !== tuple.type || previous.repo !== tuple.repo || previous.image !== tuple.image;
            if (identityChanged) {
                this.nixDockerBuild.selectedBranch.val = '';
                this.nixDockerBuild.selectedCommit.val = '';
                this.containerImage.selectedTag.val = imageVersionFromReference(tuple.image);
            }
        }
        if (this.persistedSourceKey && key === this.persistedSourceKey) {
            this.layers.val = Object.fromEntries(Object.keys(localSourceLayers(tuple))
                .map(name => [name, layer('trusted', 'Unchanged from the saved deployment.')]));
            return;
        }
        this.layers.val = localSourceLayers(tuple);
    }

    layerStatus(name) {
        return this.layers.val[name] || layer('unvalidated', '');
    }

    setLayer(name, value) {
        this.layers.val = {...this.layers.val, [name]: value};
    }

    setVersions(patch) {
        this.versions.val = {...this.versions.val, ...patch};
    }

    overallStatus() {
        return overallSourceStatus(this.layers.val);
    }

    sourceTrusted() {
        return this.overallStatus() === 'trusted';
    }

    sourceValid() {
        const status = this.overallStatus();
        return status === 'ok' || status === 'trusted';
    }

    isImage() {
        return this.form.sourceType.val === SOURCE_DOCKER_IMAGE;
    }

    isNix() {
        return this.form.sourceType.val === SOURCE_NIX_DOCKER;
    }

    // A response is current when no newer request of its kind started and the
    // tuple it was issued for is still the one on the form.
    isCurrent(kind, sequence, key) {
        return this.requestSequences[kind] === sequence && this.sourceKey === key;
    }

    // ---- validation and version listing ------------------------------------

    // validate checks the source remotely and lists its versions: image
    // access and tags, or repository access, branches, and the selected
    // branch's commits (main, or the first branch, when none is selected),
    // then the flake file at the selected commit when there is one.
    async validate() {
        this.syncSourceTuple();
        const tuple = this.currentSourceTuple();
        const key = keyOf(tuple);
        const local = localSourceLayers(tuple);
        const missing = tuple.type === SOURCE_DOCKER_IMAGE ? !tuple.image : (!tuple.repo || !tuple.flake);
        if (missing || Object.values(local).some(item => item.status === 'error')) {
            this.layers.val = local;
            return false;
        }
        const sequence = ++this.requestSequences.list;
        if (tuple.type === SOURCE_DOCKER_IMAGE) {
            this.layers.val = {image: layer('checking', 'Checking image access and listing tags...')};
            this.setVersions({loading: true, error: ''});
            try {
                const tags = await this.requestImageTags(tuple.image);
                if (!this.isCurrent('list', sequence, key)) return false;
                this.containerImage.tags.val = tags;
                this.layers.val = {image: layer('ok', `Image accessible · ${tags.length} tag${tags.length === 1 ? '' : 's'}.`)};
                this.setVersions({loaded: true, loading: false, error: ''});
                return true;
            } catch (error) {
                if (!this.isCurrent('list', sequence, key)) return false;
                this.layers.val = {image: layer('error', error.message || 'Image validation failed.')};
                this.setVersions({loading: false, error: error.message || 'Image validation failed.'});
                return false;
            }
        }
        this.layers.val = {...local, repo: layer('checking', 'Checking repository access and listing branches...')};
        this.setVersions({loading: true, error: ''});
        try {
            const listing = await this.requestNixListing(tuple.repo, this.nixDockerBuild.selectedBranch.rawVal);
            if (!this.isCurrent('list', sequence, key)) return false;
            this.publishNixListing(listing);
            this.setLayer('repo', layer('ok', `Repository accessible · ${listing.branches.length} branch${listing.branches.length === 1 ? '' : 'es'}.`));
            this.setVersions({loaded: true, loading: false, error: ''});
            const commit = this.nixDockerBuild.selectedCommit.rawVal;
            if (commit) {
                await this.checkFlakeAtCommit(commit);
            } else {
                this.setLayer('flake', layer('ok', 'Path rules ok. Existence is checked when a commit is selected.'));
            }
            return this.isCurrent('list', sequence, key) && this.sourceValid();
        } catch (error) {
            if (!this.isCurrent('list', sequence, key)) return false;
            this.setLayer('repo', layer('error', error.message || 'Repository validation failed.'));
            this.setVersions({loading: false, error: error.message || 'Repository validation failed.'});
            return false;
        }
    }

    // refreshVersions re-lists versions for a source that is already valid or
    // trusted. Failures show in the list, not in the layers.
    async refreshVersions() {
        this.syncSourceTuple();
        if (!this.sourceValid() || this.versions.val.loading) return false;
        const tuple = this.currentSourceTuple();
        const key = keyOf(tuple);
        const sequence = ++this.requestSequences.list;
        this.setVersions({loading: true, error: ''});
        try {
            if (tuple.type === SOURCE_DOCKER_IMAGE) {
                const tags = await this.requestImageTags(tuple.image);
                if (!this.isCurrent('list', sequence, key)) return false;
                this.containerImage.tags.val = tags;
            } else {
                const listing = await this.requestNixListing(tuple.repo, this.nixDockerBuild.selectedBranch.rawVal);
                if (!this.isCurrent('list', sequence, key)) return false;
                this.publishNixListing(listing);
            }
            this.setVersions({loaded: true, loading: false, error: ''});
            return true;
        } catch (error) {
            if (!this.isCurrent('list', sequence, key)) return false;
            this.setVersions({loading: false, error: error.message || 'Failed to load versions.'});
            return false;
        }
    }

    async selectBranch(branch) {
        this.syncSourceTuple();
        const selected = (branch || '').trim();
        if (!this.isNix() || !selected || this.versions.val.loading) return false;
        const tuple = this.currentSourceTuple();
        const key = keyOf(tuple);
        const sequence = ++this.requestSequences.list;
        this.nixDockerBuild.selectedBranch.val = selected;
        this.nixDockerBuild.commits.val = [];
        this.setVersions({loading: true, error: ''});
        try {
            const commits = await this.requestNixCommits(tuple.repo, selected);
            if (!this.isCurrent('list', sequence, key)) return false;
            this.nixDockerBuild.commits.val = commits;
            this.setVersions({loaded: true, loading: false, error: ''});
            return true;
        } catch (error) {
            if (!this.isCurrent('list', sequence, key)) return false;
            this.setVersions({loading: false, error: error.message || 'Failed to load commits.'});
            return false;
        }
    }

    // selectVersion records the pick; for Nix it also checks the flake file
    // at that commit unless the source is trusted.
    selectVersion(id) {
        this.syncSourceTuple();
        const version = (id || '').trim();
        if (this.isImage()) {
            this.containerImage.selectedTag.val = version;
            return;
        }
        this.nixDockerBuild.selectedCommit.val = version;
        void this.checkFlakeAtCommit(version);
    }

    // ensureVersionsLoaded lists lazily for a source that is valid but whose
    // lists were never fetched: a trusted update opened straight into the
    // version dropdown. The saved deployment's versions endpoint also names
    // the branch that carries the deployed commit.
    async ensureVersionsLoaded() {
        this.syncSourceTuple();
        if (!this.sourceValid() || this.versions.val.loaded || this.versions.val.loading) return false;
        const deploymentId = Number(this.existingState?.id || 0);
        if (!this.sourceTrusted() || !this.loadDeploymentVersions || !deploymentId) return this.refreshVersions();
        const tuple = this.currentSourceTuple();
        const key = keyOf(tuple);
        const sequence = ++this.requestSequences.list;
        this.setVersions({loading: true, error: ''});
        try {
            const response = await this.loadDeploymentVersions({deploymentId});
            if (!this.isCurrent('list', sequence, key)) return false;
            if (Number(response?.deploymentId || 0) !== deploymentId) {
                throw new Error('Version response did not attest the requested deployment.');
            }
            if (tuple.type === SOURCE_NIX_DOCKER) {
                const result = response?.nixDockerBuild;
                if (!result) throw new Error('Version response did not include Nix versions.');
                this.publishNixListing({
                    branches: result.branches || [],
                    branch: (result.selectedBranch || '').trim(),
                    commits: result.commits || [],
                });
            } else {
                const result = response?.containerImage;
                if (!result) throw new Error('Version response did not include image tags.');
                this.containerImage.tags.val = result.tags || [];
            }
            this.setVersions({loaded: true, loading: false, error: ''});
            return true;
        } catch (error) {
            if (!this.isCurrent('list', sequence, key)) return false;
            this.setVersions({loading: false, error: error.message || 'Failed to load deployment versions.'});
            return false;
        }
    }

    async checkFlakeAtCommit(commit) {
        this.syncSourceTuple();
        const tuple = this.currentSourceTuple();
        const key = keyOf(tuple);
        if (tuple.type !== SOURCE_NIX_DOCKER || !FULL_GIT_COMMIT_RE.test(commit || '')) return false;
        if (this.layerStatus('flake').status === 'trusted' || !validateLocalFlakePath(tuple.flake).ok) return false;
        const sequence = ++this.requestSequences.exact;
        this.setLayer('flake', layer('checking', `Checking ${tuple.flake} at ${shortID(commit)}...`));
        const stillWanted = () => this.isCurrent('exact', sequence, key) && this.nixDockerBuild.selectedCommit.rawVal === commit;
        try {
            const response = await this.validateSource(buildExactNixValidationRequest(tuple.repo, commit, tuple.flake));
            if (!stillWanted()) return false;
            const result = attestExactNixValidationResponse(response, tuple.repo, commit, tuple.flake);
            if (!result) throw new Error('Validation response did not attest the current repository, commit, and flake path.');
            if (!result.gitRepository.ok) throw new Error(result.gitRepository.message || 'Repository is not accessible.');
            if (!result.commitCheck.ok) throw new Error(result.commitCheck.message || 'Commit is not accessible.');
            if (!result.nixFlakeFile.ok) throw new Error(result.nixFlakeFile.message || 'Flake path is not a regular file at this commit.');
            this.setLayer('flake', layer('ok', result.nixFlakeFile.message || `flake.nix found at ${shortID(commit)}.`));
            return true;
        } catch (error) {
            if (!stillWanted()) return false;
            this.setLayer('flake', layer('error', error.message || 'Exact source validation failed.'));
            return false;
        }
    }

    // ---- requests ------------------------------------------------------------

    async requestImageTags(image) {
        const response = await this.validateSource(buildImageDiscoveryRequest(image, {refresh: true}));
        const result = attestImageDiscoveryResponse(response);
        if (!result) throw new Error('Image response did not attest the requested image check.');
        if (!result.image.ok) throw new Error(result.image.message || 'Image is not accessible.');
        return result.tags || [];
    }

    // requestNixListing returns {branches, branch, commits}. With a branch
    // already chosen one request lists everything; otherwise the repository
    // is listed first and the commits of main (or the first branch) second.
    async requestNixListing(repo, preferredBranch) {
        const chooseBranch = branches => (branches.includes(preferredBranch) ? preferredBranch
            : (branches.includes('main') ? 'main' : (branches[0] || '')));
        if (preferredBranch) {
            const response = await this.validateSource(buildNixListingRequest(repo, preferredBranch));
            const result = attestNixCommitDiscoveryResponse(response, repo, preferredBranch);
            if (!result) throw new Error('Repository response did not attest the requested repository and branch.');
            if (!result.gitRepository.ok) throw new Error(result.gitRepository.message || 'Repository is not accessible.');
            if (!result.availableBranches?.loaded || result.availableBranches?.errormessage) {
                throw new Error(result.availableBranches?.errormessage || 'Unable to list repository branches.');
            }
            const branches = result.availableBranches.branches || [];
            if (result.branchCheck.ok && result.availableCommits?.loaded && !result.availableCommits?.errormessage) {
                return {branches, branch: preferredBranch, commits: result.availableCommits.commits || []};
            }
            // The remembered branch is gone; fall back to another one.
            const branch = chooseBranch(branches.filter(name => name !== preferredBranch));
            if (!branch) throw new Error(result.branchCheck.message || 'Repository has no branches.');
            return {branches, branch, commits: await this.requestNixCommits(repo, branch)};
        }
        const response = await this.validateSource(buildNixRepositoryDiscoveryRequest(repo));
        const result = attestNixRepositoryResponse(response, repo);
        if (!result) throw new Error('Repository response did not attest the requested repository.');
        if (!result.gitRepository.ok) throw new Error(result.gitRepository.message || 'Repository is not accessible.');
        if (!result.availableBranches?.loaded || result.availableBranches?.errormessage) {
            throw new Error(result.availableBranches?.errormessage || 'Unable to list repository branches.');
        }
        const branches = result.availableBranches.branches || [];
        const branch = chooseBranch(branches);
        if (!branch) return {branches, branch: '', commits: []};
        return {branches, branch, commits: await this.requestNixCommits(repo, branch)};
    }

    async requestNixCommits(repo, branch) {
        const response = await this.validateSource(buildNixCommitDiscoveryRequest(repo, branch));
        const result = attestNixCommitDiscoveryResponse(response, repo, branch);
        if (!result) throw new Error('Commit response did not attest the requested repository and branch.');
        if (!result.gitRepository.ok) throw new Error(result.gitRepository.message || 'Repository is not accessible.');
        if (!result.branchCheck.ok) throw new Error(result.branchCheck.message || 'Branch is not accessible.');
        if (!result.availableCommits?.loaded || result.availableCommits?.errormessage) {
            throw new Error(result.availableCommits?.errormessage || 'Unable to list branch commits.');
        }
        return result.availableCommits.commits || [];
    }

    publishNixListing(listing) {
        this.nixDockerBuild.branches.val = listing.branches;
        this.nixDockerBuild.selectedBranch.val = listing.branch;
        this.nixDockerBuild.commits.val = listing.commits;
    }

    // ---- selection and labels ------------------------------------------------

    setDesiredRunning(running) {
        this.desiredRunning.val = Boolean(running);
    }

    explicitImageVersion() {
        return this.isImage() ? imageVersionFromReference(this.form.containerImage.val) : '';
    }

    selectedTargetVersion() {
        if (this.isImage()) {
            return this.explicitImageVersion() || this.containerImage.selectedTag.val.trim();
        }
        if (this.isNix()) return this.nixDockerBuild.selectedCommit.val.trim();
        return '';
    }

    deployedVersion() {
        return this.existingState?.deployedVersion || '';
    }

    // versionEntry is the loaded list entry for the selected version, when the
    // lists hold it, so labels can add its branch, message, and date.
    versionEntry() {
        const selected = this.selectedTargetVersion();
        if (!selected) return null;
        const list = this.isImage() ? this.containerImage.tags.val : this.nixDockerBuild.commits.val;
        return list.find(item => item?.id === selected) || null;
    }

    // versionOptions feeds the Code-mode completion for `version = "…"`.
    versionOptions() {
        const dateOf = version => (version?.time instanceof Date && version.time.getTime() > 0 ? version.time.toISOString().slice(0, 10) : '');
        if (this.isImage()) {
            return this.containerImage.tags.val.map(tag => ({label: tag.id, apply: tag.id, detail: [tag.label, dateOf(tag)].filter(Boolean).join(' · ')}));
        }
        const branch = this.nixDockerBuild.selectedBranch.val;
        return this.nixDockerBuild.commits.val.map(commit => ({
            label: `${shortID(commit.id)} ${commit.label || ''}`.trim(),
            apply: commit.id,
            detail: [branch, dateOf(commit)].filter(Boolean).join(' · '),
        }));
    }

    sourceMatchesInitial() {
        return keyOf(this.currentSourceTuple()) === keyOf(this.initialSource);
    }

    createDesiredVersion() {
        return this.selectedTargetVersion()
            || (this.existingState?.deployedVersion && this.sourceMatchesInitial() ? this.existingState.deployedVersion : '');
    }

    // ---- save gating -----------------------------------------------------------

    // sourceInvalidReason blocks any save on a source the checks rejected, and
    // while a check is running.
    sourceInvalidReason() {
        const layers = this.layers.val;
        for (const [name, item] of Object.entries(layers)) {
            if (item.status === 'error') return `${LAYER_NAMES[name] || name}: ${item.message}`;
        }
        if (Object.values(layers).some(item => item.status === 'checking')) return 'Validating the source.';
        return '';
    }

    // versionInvalidReason applies whether or not the deployment runs: a Nix
    // version is a full commit sha or nothing.
    versionInvalidReason() {
        if (!this.isNix()) return '';
        const commit = this.nixDockerBuild.selectedCommit.val.trim();
        if (commit && !FULL_GIT_COMMIT_RE.test(commit)) return 'Version must be a full 40-character commit sha.';
        return '';
    }

    // runningInvalidReason: a running deployment needs a validated or trusted
    // source and a selected version.
    runningInvalidReason() {
        if (!this.desiredRunning.val) return '';
        if (this.isNix()) {
            const local = validateLocalFlakePath(this.form.nixFlake.val);
            if (!local.ok) return local.message;
        }
        if (!this.sourceValid()) {
            return this.overallStatus() === 'checking'
                ? 'Validating the source.'
                : 'Validate the source before setting the deployment to Running.';
        }
        const version = this.createDesiredVersion();
        if (!version) return 'Select a version before setting the deployment to Running.';
        if (this.isNix() && !FULL_GIT_COMMIT_RE.test(version)) {
            return 'Select a full 40-character commit before setting the deployment to Running.';
        }
        return '';
    }

    // ---- document boundary ---------------------------------------------------

    toDocument() {
        const version = this.createDesiredVersion();
        const running = Boolean(this.desiredRunning.val);
        return {
            identity: formToDeploymentIdentity(this.form),
            nodeId: Number(this.form.nodeId.val || 0),
            spec: formToSpec(this.form, {version, running}),
        };
    }

    replaceDocument(document) {
        const identity = document?.identity || {};
        const spec = document?.spec || {};
        const workload = spec.container1Spec || spec.opendeploySpec || {};
        replaceDeploymentFormFromConfig(this.form, {
            id: Number(this.form.deploymentId.val || 0),
            def: {
                name: identity.name || '',
                spaceId: Number(identity.spaceId || 0),
                nodeId: Number(document?.nodeId || 0),
                spec,
            },
        });
        // Reset the layers now, before the selection below, so the derive
        // pass that follows sees the tuple already handled.
        this.syncSourceTuple();
        this.desiredRunning.val = spec.opendeploySpec ? true : Boolean(workload.running);
        const version = (workload.version || '').trim();
        if (this.isImage()) {
            this.containerImage.selectedTag.val = version;
        } else if (this.isNix()) {
            const previous = this.nixDockerBuild.selectedCommit.rawVal;
            this.nixDockerBuild.selectedCommit.val = version;
            if (version && version !== previous && this.sourceValid()) void this.checkFlakeAtCommit(version);
        }
        this.documentRevision.val += 1;
    }

    toCreatePayload() {
        const version = this.createDesiredVersion();
        const running = Boolean(this.desiredRunning.val);
        const identity = formToDeploymentIdentity(this.form);
        return {
            name: identity.name,
            spaceId: identity.spaceId,
            nodeId: Number(this.form.nodeId.val || 0),
            spec: formToSpec(this.form, {version, running}),
        };
    }

    // toMovePayload returns a DeploymentUpdateRequestV2 assigning a new space
    // when the form names a different space than the deployment currently has,
    // else null.
    toMovePayload() {
        if (!this.existingState) throw new Error('Cannot produce move payload without existing deployment state');
        const nextSpaceId = Number(this.form.spaceId.val || 0);
        if (!nextSpaceId || nextSpaceId === Number(this.existingState.spaceId || 0)) return null;
        return {
            deploymentId: this.existingState.id,
            expectedVersion: Number(this.existingState.version || 0) + 1,
            assignedSpaceUpdate: {spaceId: nextSpaceId},
        };
    }

    // toUpdatePayload returns a DeploymentUpdateRequestV2 carrying the single
    // kind of change the form implies, or null when there is nothing to send.
    toUpdatePayload() {
        if (!this.existingState) throw new Error('Cannot produce update payload without existing deployment state');
        const payload = {
            deploymentId: this.existingState.id,
            expectedVersion: Number(this.existingState.version || 0) + 1,
        };
        const nextSpec = formToSpec(this.form);
        if (JSON.stringify(nextSpec) !== this.initialSpecKey) {
            payload.specUpdate = {
                spec: formToSpec(this.form, {
                    version: this.createDesiredVersion(),
                    running: Boolean(this.desiredRunning.val),
                }),
            };
            return payload;
        }
        const targetVersion = this.selectedTargetVersion();
        const versionChanged = Boolean(targetVersion) && targetVersion !== (this.existingState.deployedVersion || '');
        if (!this.desiredRunning.val) {
            // A stopped deployment may still retarget its version for the
            // next start. The version-only update always marks the workload
            // running, so a stopped retarget goes as a spec update carrying
            // running=false; a plain stop keeps the running-only path, which
            // preserves the version.
            if (versionChanged) {
                payload.specUpdate = {spec: formToSpec(this.form, {version: targetVersion, running: false})};
                return payload;
            }
            if (this.existingState.desiredRunning) {
                payload.runningOnlyUpdate = {desiredRunning: false};
                return payload;
            }
            return null;
        }
        if (targetVersion && (versionChanged || !this.existingState.desiredRunning)) {
            payload.versionOnlyUpdate = {targetVersion};
            return payload;
        }
        return null;
    }
}
