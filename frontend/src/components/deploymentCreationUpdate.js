import van from "vanjs-core";
import {
    deploymentConfigToForm,
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
    buildNixRepositoryDiscoveryRequest,
    FULL_GIT_COMMIT_RE,
    imageDiscoveryKey,
    imageVersionFromReference,
    nixCommitDiscoveryKey,
    nixExactValidationKey,
    nixRepositoryDiscoveryKey,
    SOURCE_DOCKER_IMAGE,
    SOURCE_NIX_DOCKER,
    validateLocalFlakePath,
} from "./deploymentSource.js";

export {SOURCE_DOCKER_IMAGE, SOURCE_NIX_DOCKER} from "./deploymentSource.js";

const idleStatus = () => ({status: 'idle', message: '', key: ''});
const checkingStatus = (key, message) => ({status: 'checking', message, key});
const errorStatus = (key, message) => ({status: 'error', message, key});
const okStatus = (key, message) => ({status: 'ok', message, key});

export class DeploymentCreationUpdate {
    constructor({mode = null, deployment = null, deploymentConfig = null, validateSource} = {}) {
        if (typeof validateSource !== 'function') throw new Error('validateSource is required');
        const editorMode = mode || (deployment ? 'update' : 'create');
        if (editorMode !== 'create' && editorMode !== 'update') throw new Error(`Unsupported deployment editor mode: ${editorMode}`);
        this.validateSource = validateSource;
        this.mode = editorMode;
        this.existingState = deployment;
        this.form = deploymentConfig ? deploymentConfigToForm(deploymentConfig) : emptyDeploymentForm();
        const workload = deploymentConfig?.spec?.container1Spec || deploymentConfig?.spec?.systemdSpec;
        const initialRunning = editorMode === 'create'
            ? (deployment ? Boolean(deployment.desiredRunning) : true)
            : (workload ? Boolean(workload.running) : Boolean(deployment?.desiredRunning));
        this.desiredRunning = van.state(initialRunning);
        this.documentRevision = van.state(0);
        this.initialSpecKey = JSON.stringify(formToSpec(this.form));
        this.initialSource = this.persistedSource();

        const configuredVersion = workload?.version || deployment?.deployedVersion || '';
        const deployedNixVersion = deployment?.variant === SOURCE_NIX_DOCKER ? configuredVersion : '';
        const explicitImageVersion = imageVersionFromReference(this.form.containerImage.val);
        const deployedImageVersion = deployment?.variant === SOURCE_DOCKER_IMAGE ? configuredVersion : '';
        this.nixDockerBuild = {
            selectedBranch: van.state(''),
            selectedCommit: van.state(deployedNixVersion),
            branches: van.state([]),
            commits: van.state([]),
            repository: van.state(idleStatus()),
            commitDiscovery: van.state(idleStatus()),
            exactValidation: van.state(idleStatus()),
        };
        this.containerImage = {
            selectedTag: van.state(explicitImageVersion || deployedImageVersion),
            tags: van.state(deployedImageVersion ? [{id: deployedImageVersion, label: 'Current'}] : []),
            discovery: van.state(idleStatus()),
        };
        this.loadingVersions = van.state(false);
        this.versionRequestDescription = van.state('');
        this.requestSequences = {repository: 0, commits: 0, exact: 0, image: 0, versions: 0};
        this.flakeValidationTimer = null;
        this.document = {
            read: () => this.toDocument(),
            replace: document => this.replaceDocument(document),
            revision: this.documentRevision,
        };
    }

    persistedSource() {
        return {
            type: this.form.sourceType.val,
            repo: this.form.nixRepo.val.trim(),
            flake: this.form.nixFlake.val.trim(),
            image: this.form.containerImage.val.trim(),
        };
    }

    currentSourceID() {
        return this.form.sourceType.val === SOURCE_DOCKER_IMAGE
            ? this.form.containerImage.val.trim()
            : this.form.nixRepo.val.trim();
    }

    deploymentVersionsKey(deploymentId) {
        const sourceType = this.form.sourceType.val;
        const source = sourceType === SOURCE_NIX_DOCKER
            ? this.form.nixRepo.val.trim()
            : (sourceType === SOURCE_DOCKER_IMAGE ? this.form.containerImage.val.trim() : '');
        return JSON.stringify(['deployment-versions', Number(deploymentId || 0), sourceType, source]);
    }

    repositoryStatus() {
        const key = nixRepositoryDiscoveryKey(this.form.nixRepo.val);
        const status = this.nixDockerBuild.repository.val;
        if (this.form.sourceType.val !== SOURCE_NIX_DOCKER) return idleStatus();
        if (status.key === key && status.status !== 'error') return status;
        if (this.initialRepositoryTrusted()) return okStatus(key, 'Current repository.');
        return status.key === key ? status : idleStatus();
    }

    flakeStatus() {
        if (this.form.sourceType.val !== SOURCE_NIX_DOCKER || !this.form.nixFlake.val.trim()) return idleStatus();
        if (this.initialNixSourceTrusted()) {
            return okStatus(nixExactValidationKey(
                this.form.nixRepo.val,
                this.nixDockerBuild.selectedCommit.val,
                this.form.nixFlake.val,
            ), 'Current source.');
        }
        const local = validateLocalFlakePath(this.form.nixFlake.val);
        if (!local.ok) return errorStatus('', local.message);
        const key = nixExactValidationKey(
            this.form.nixRepo.val,
            this.nixDockerBuild.selectedCommit.val,
            this.form.nixFlake.val,
        );
        const status = this.nixDockerBuild.exactValidation.val;
        return status.key === key ? status : idleStatus();
    }

    imageStatus() {
        const key = imageDiscoveryKey(this.form.containerImage.val);
        const status = this.containerImage.discovery.val;
        if (this.form.sourceType.val !== SOURCE_DOCKER_IMAGE) return idleStatus();
        if (status.key === key && status.status !== 'error') return status;
        if (this.initialImageTrusted()) return okStatus(key, 'Current image.');
        return status.key === key ? status : idleStatus();
    }

    sourcePathInvalidReason() {
        if (this.form.sourceType.val === SOURCE_DOCKER_IMAGE && this.imageStatus().status === 'error') {
            return 'Image path invalid.';
        }
        if (this.form.sourceType.val === SOURCE_NIX_DOCKER && this.repositoryStatus().status === 'error') {
            return 'Nix repository path invalid.';
        }
        return '';
    }

    cancel(kind) {
        this.requestSequences[kind] += 1;
    }

    isCurrent(kind, sequence, key, currentKey) {
        return this.requestSequences[kind] === sequence && key === currentKey();
    }

    updateVersionActivity() {
        const states = [
            this.nixDockerBuild.repository.val,
            this.nixDockerBuild.commitDiscovery.val,
            this.nixDockerBuild.exactValidation.val,
            this.containerImage.discovery.val,
        ];
        this.loadingVersions.val = states.some(state => state.status === 'checking');
        if (!this.loadingVersions.val) this.versionRequestDescription.val = '';
    }

    invalidateExactValidation() {
        this.cancel('exact');
        this.nixDockerBuild.exactValidation.val = idleStatus();
        this.updateVersionActivity();
    }

    cancelSourceRequests() {
        for (const kind of Object.keys(this.requestSequences)) this.cancel(kind);
        if (this.nixDockerBuild.repository.val.status === 'checking') this.nixDockerBuild.repository.val = idleStatus();
        if (this.nixDockerBuild.commitDiscovery.val.status === 'checking') this.nixDockerBuild.commitDiscovery.val = idleStatus();
        if (this.nixDockerBuild.exactValidation.val.status === 'checking') this.nixDockerBuild.exactValidation.val = idleStatus();
        if (this.containerImage.discovery.val.status === 'checking') this.containerImage.discovery.val = idleStatus();
        this.updateVersionActivity();
    }

    onSourceTypeChange() {
        this.cancel('versions');
        this.cancel('repository');
        this.cancel('commits');
        this.cancel('image');
        this.invalidateExactValidation();
        this.nixDockerBuild.repository.val = idleStatus();
        this.nixDockerBuild.commitDiscovery.val = idleStatus();
        this.containerImage.discovery.val = idleStatus();
        this.nixDockerBuild.branches.val = [];
        this.nixDockerBuild.commits.val = [];
        this.nixDockerBuild.selectedBranch.val = '';
        this.nixDockerBuild.selectedCommit.val = '';
        this.containerImage.tags.val = [];
        this.containerImage.selectedTag.val = imageVersionFromReference(this.form.containerImage.val);
        this.updateVersionActivity();
    }

    onRepositoryInput() {
        this.cancel('versions');
        this.cancel('repository');
        this.cancel('commits');
        this.invalidateExactValidation();
        this.nixDockerBuild.repository.val = idleStatus();
        this.nixDockerBuild.commitDiscovery.val = idleStatus();
        this.nixDockerBuild.branches.val = [];
        this.nixDockerBuild.commits.val = [];
        this.nixDockerBuild.selectedBranch.val = '';
        this.nixDockerBuild.selectedCommit.val = '';
        this.updateVersionActivity();
    }

    onFlakeInput() {
        this.invalidateExactValidation();
        if (this.flakeValidationTimer) clearTimeout(this.flakeValidationTimer);
        if (this.desiredRunning.val && this.nixDockerBuild.selectedCommit.val) {
            this.flakeValidationTimer = setTimeout(() => void this.validateExactNixSelection(), 250);
            this.flakeValidationTimer.unref?.();
        }
    }

    onFlakeBlur() {
        if (this.flakeValidationTimer) clearTimeout(this.flakeValidationTimer);
        this.flakeValidationTimer = null;
        if (this.desiredRunning.val) void this.validateExactNixSelection();
    }

    onImageInput() {
        this.cancel('versions');
        this.cancel('image');
        this.containerImage.discovery.val = idleStatus();
        this.containerImage.tags.val = [];
        this.containerImage.selectedTag.val = imageVersionFromReference(this.form.containerImage.val);
        this.updateVersionActivity();
    }

    async onRepositoryBlur() {
        return this.loadVersions({preserveSelection: true});
    }

    async onImageBlur() {
        if (!this.form.containerImage.val.trim() || imageVersionFromReference(this.form.containerImage.val)) return;
        return this.discoverImageVersions({preserveSelection: true});
    }

    async discoverRepository({refresh = true} = {}) {
        const repo = this.form.nixRepo.val.trim();
        if (!repo) return false;
        const key = nixRepositoryDiscoveryKey(repo);
        const sequence = ++this.requestSequences.repository;
        this.nixDockerBuild.repository.val = checkingStatus(key, 'Checking repository access...');
        this.versionRequestDescription.val = 'Refreshing available branches.';
        this.updateVersionActivity();
        try {
            const response = await this.validateSource(buildNixRepositoryDiscoveryRequest(repo, {refresh}));
            if (!this.isCurrent('repository', sequence, key, () => nixRepositoryDiscoveryKey(this.form.nixRepo.val))) return false;
            const result = attestNixRepositoryResponse(response, repo);
            if (!result) throw new Error('Repository response did not attest the requested repository.');
            if (!result.gitRepository.ok) throw new Error(result.gitRepository.message || 'Repository is not accessible.');
            if (!result.availableBranches?.loaded || result.availableBranches?.errormessage) {
                throw new Error(result.availableBranches?.errormessage || 'Unable to list repository branches.');
            }
            const branches = result.availableBranches.branches || [];
            this.nixDockerBuild.branches.val = branches;
            const current = this.nixDockerBuild.selectedBranch.val;
            this.nixDockerBuild.selectedBranch.val = branches.includes(current)
                ? current
                : (branches.includes('main') ? 'main' : (branches[0] || ''));
            this.nixDockerBuild.repository.val = okStatus(key, result.gitRepository.message || 'Repository accessible.');
            return true;
        } catch (error) {
            if (!this.isCurrent('repository', sequence, key, () => nixRepositoryDiscoveryKey(this.form.nixRepo.val))) return false;
            const message = error.message || 'Repository validation failed.';
            this.nixDockerBuild.repository.val = errorStatus(key, message);
            this.nixDockerBuild.branches.val = [];
            this.nixDockerBuild.commits.val = [];
            return false;
        } finally {
            this.updateVersionActivity();
        }
    }

    async discoverCommits(branch, {preserveSelection = false, refresh = true} = {}) {
        const repo = this.form.nixRepo.val.trim();
        const selectedBranch = (branch || '').trim();
        if (!repo || !selectedBranch) return false;
        const key = nixCommitDiscoveryKey(repo, selectedBranch);
        const sequence = ++this.requestSequences.commits;
        this.nixDockerBuild.commitDiscovery.val = checkingStatus(key, 'Loading commits...');
        this.versionRequestDescription.val = 'Refreshing available commits.';
        this.updateVersionActivity();
        try {
            const response = await this.validateSource(buildNixCommitDiscoveryRequest(repo, selectedBranch, {refresh}));
            if (!this.isCurrent('commits', sequence, key, () => nixCommitDiscoveryKey(this.form.nixRepo.val, this.nixDockerBuild.selectedBranch.val))) return false;
            const result = attestNixCommitDiscoveryResponse(response, repo, selectedBranch);
            if (!result) throw new Error('Commit response did not attest the requested repository and branch.');
            if (!result.gitRepository.ok) throw new Error(result.gitRepository.message || 'Repository is not accessible.');
            if (!result.branchCheck.ok) throw new Error(result.branchCheck.message || 'Branch is not accessible.');
            if (!result.availableCommits?.loaded || result.availableCommits?.errormessage) {
                throw new Error(result.availableCommits?.errormessage || 'Unable to list branch commits.');
            }
            const commits = result.availableCommits.commits || [];
            const previous = this.nixDockerBuild.selectedCommit.val;
            const deployed = this.existingState?.variant === SOURCE_NIX_DOCKER ? (this.existingState.deployedVersion || '') : '';
            const stoppedUpdateWithChangedSource = Boolean(this.existingState && !this.desiredRunning.val && !this.sourceMatchesInitial());
            this.nixDockerBuild.commits.val = commits;
            let selectedCommit;
            if (preserveSelection && previous) {
                selectedCommit = previous;
            } else if (preserveSelection && deployed && this.sourceMatchesInitial()) {
                selectedCommit = deployed;
            } else if (stoppedUpdateWithChangedSource) {
                selectedCommit = '';
            } else {
                selectedCommit = commits[0]?.id || '';
            }
            this.nixDockerBuild.selectedCommit.val = selectedCommit;
            if (selectedCommit !== previous) {
                this.invalidateExactValidation();
            }
            this.nixDockerBuild.commitDiscovery.val = okStatus(key, 'Commits loaded.');
            return true;
        } catch (error) {
            if (!this.isCurrent('commits', sequence, key, () => nixCommitDiscoveryKey(this.form.nixRepo.val, this.nixDockerBuild.selectedBranch.val))) return false;
            const message = error.message || 'Failed to load commits.';
            this.nixDockerBuild.commitDiscovery.val = errorStatus(key, message);
            this.nixDockerBuild.commits.val = [];
            return false;
        } finally {
            this.updateVersionActivity();
        }
    }

    async selectBranch(branch) {
        this.nixDockerBuild.selectedBranch.val = branch;
        this.nixDockerBuild.commits.val = [];
        const loaded = await this.discoverCommits(branch, {preserveSelection: true});
        if (loaded && this.desiredRunning.val
            && !this.initialNixSourceTrusted() && !this.hasCurrentExactNixValidation()) {
            await this.validateExactNixSelection();
        }
        return loaded;
    }

    selectCommit(commit) {
        this.nixDockerBuild.selectedCommit.val = (commit || '').trim();
        this.invalidateExactValidation();
        if (this.desiredRunning.val) void this.validateExactNixSelection();
    }

    async validateExactNixSelection() {
        this.invalidateExactValidation();
        if (!this.desiredRunning.val || this.form.sourceType.val !== SOURCE_NIX_DOCKER) return false;
        const repo = this.form.nixRepo.val.trim();
        const commit = this.nixDockerBuild.selectedCommit.val.trim();
        const flakePath = this.form.nixFlake.val.trim();
        if (!repo || !FULL_GIT_COMMIT_RE.test(commit) || !validateLocalFlakePath(flakePath).ok) return false;
        if (this.initialNixSourceTrusted()) return true;
        const key = nixExactValidationKey(repo, commit, flakePath);
        const sequence = ++this.requestSequences.exact;
        this.nixDockerBuild.exactValidation.val = checkingStatus(key, 'Validating exact commit and flake...');
        this.versionRequestDescription.val = 'Validating exact commit and flake.';
        this.updateVersionActivity();
        try {
            const response = await this.validateSource(buildExactNixValidationRequest(repo, commit, flakePath));
            if (!this.isCurrent('exact', sequence, key, () => nixExactValidationKey(
                this.form.nixRepo.val,
                this.nixDockerBuild.selectedCommit.val,
                this.form.nixFlake.val,
            ))) return false;
            const result = attestExactNixValidationResponse(response, repo, commit, flakePath);
            if (!result) throw new Error('Validation response did not attest the current repository, commit, and flake path.');
            if (!result.gitRepository.ok) throw new Error(result.gitRepository.message || 'Repository is not accessible.');
            if (!result.commitCheck.ok) throw new Error(result.commitCheck.message || 'Commit is not accessible.');
            if (!result.nixFlakeFile.ok) throw new Error(result.nixFlakeFile.message || 'Flake path is not a regular file at this commit.');
            this.nixDockerBuild.exactValidation.val = okStatus(key, result.nixFlakeFile.message || 'Commit and flake verified.');
            return true;
        } catch (error) {
            if (!this.isCurrent('exact', sequence, key, () => nixExactValidationKey(
                this.form.nixRepo.val,
                this.nixDockerBuild.selectedCommit.val,
                this.form.nixFlake.val,
            ))) return false;
            this.nixDockerBuild.exactValidation.val = errorStatus(key, error.message || 'Exact source validation failed.');
            return false;
        } finally {
            this.updateVersionActivity();
        }
    }

    hasCurrentExactNixValidation() {
        const key = nixExactValidationKey(
            this.form.nixRepo.val,
            this.nixDockerBuild.selectedCommit.val,
            this.form.nixFlake.val,
        );
        const status = this.nixDockerBuild.exactValidation.val;
        return status.status === 'ok' && status.key === key;
    }

    runningNixInvalidReason() {
        if (this.form.sourceType.val !== SOURCE_NIX_DOCKER) return '';
        const commit = this.nixDockerBuild.selectedCommit.val.trim();
        if (commit && !FULL_GIT_COMMIT_RE.test(commit)) return 'Nix versions must be full 40-character commits.';
        if (!this.desiredRunning.val) return '';
        if (!FULL_GIT_COMMIT_RE.test(commit)) return 'Select a full 40-character commit before setting the deployment to Running.';
        const local = validateLocalFlakePath(this.form.nixFlake.val);
        if (!local.ok) return local.message;
        if (this.initialNixSourceTrusted()) return '';
        if (this.hasCurrentExactNixValidation()) return '';
        const status = this.flakeStatus();
        if (status.status === 'checking') return 'Validating the selected commit and flake path.';
        return status.message || 'Validate the selected commit and flake path before running the deployment.';
    }

    async discoverImageVersions({preserveSelection = true, refresh = true} = {}) {
        const image = this.form.containerImage.val.trim();
        if (!image) return false;
        const key = imageDiscoveryKey(image);
        const sequence = ++this.requestSequences.image;
        this.containerImage.discovery.val = checkingStatus(key, 'Checking image...');
        this.versionRequestDescription.val = 'Refreshing available tags.';
        this.updateVersionActivity();
        try {
            const response = await this.validateSource(buildImageDiscoveryRequest(image, {refresh}));
            if (!this.isCurrent('image', sequence, key, () => imageDiscoveryKey(this.form.containerImage.val))) return false;
            const result = attestImageDiscoveryResponse(response);
            if (!result) throw new Error('Image response did not attest the requested image check.');
            if (!result.image.ok) throw new Error(result.image.message || 'Image is not accessible.');
            const tags = result.tags || [];
            const previous = this.containerImage.selectedTag.val;
            const deployed = this.existingState?.variant === SOURCE_DOCKER_IMAGE ? (this.existingState.deployedVersion || '') : '';
            const stoppedUpdateWithChangedSource = Boolean(this.existingState && !this.desiredRunning.val && !this.sourceMatchesInitial());
            this.containerImage.tags.val = tags;
            if (preserveSelection && previous && (tags.some(item => item.id === previous) || previous === deployed)) {
                this.containerImage.selectedTag.val = previous;
            } else if (preserveSelection && deployed && this.sourceMatchesInitial()) {
                this.containerImage.selectedTag.val = deployed;
            } else if (stoppedUpdateWithChangedSource) {
                this.containerImage.selectedTag.val = '';
            } else {
                this.containerImage.selectedTag.val = tags[0]?.id || '';
            }
            this.containerImage.discovery.val = okStatus(key, result.image.message || 'Image accessible.');
            return true;
        } catch (error) {
            if (!this.isCurrent('image', sequence, key, () => imageDiscoveryKey(this.form.containerImage.val))) return false;
            const message = error.message || 'Failed to load image tags.';
            this.containerImage.discovery.val = errorStatus(key, message);
            this.containerImage.tags.val = [];
            return false;
        } finally {
            this.updateVersionActivity();
        }
    }

    async loadExistingDeploymentVersions(action, deploymentId, {preserveSelection = true} = {}) {
        if (typeof action !== 'function') throw new Error('loadDeploymentVersions is required');
        const sourceType = this.form.sourceType.val;
        const key = this.deploymentVersionsKey(deploymentId);
        const sequence = ++this.requestSequences.versions;
        this.versionRequestDescription.val = 'Loading available versions.';
        if (sourceType === SOURCE_NIX_DOCKER) {
            this.nixDockerBuild.repository.val = checkingStatus(
                nixRepositoryDiscoveryKey(this.form.nixRepo.val),
                'Loading repository versions...',
            );
            this.nixDockerBuild.commitDiscovery.val = checkingStatus(key, 'Loading commits...');
        } else if (sourceType === SOURCE_DOCKER_IMAGE) {
            this.containerImage.discovery.val = checkingStatus(imageDiscoveryKey(this.form.containerImage.val), 'Loading tags...');
        }
        this.updateVersionActivity();
        const isCurrent = () => this.requestSequences.versions === sequence
            && this.deploymentVersionsKey(deploymentId) === key;
        try {
            const response = await action({deploymentId});
            if (!isCurrent()) return false;
            if (Number(response?.deploymentId || 0) !== Number(deploymentId || 0)) {
                throw new Error('Version response did not attest the requested deployment.');
            }

            if (sourceType === SOURCE_NIX_DOCKER) {
                const result = response?.nixDockerBuild;
                if (!result) throw new Error('Version response did not include Nix versions.');
                const branches = result.branches || [];
                const selectedBranch = (result.selectedBranch || '').trim();
                const commits = result.commits || [];
                const previous = this.nixDockerBuild.selectedCommit.val;
                const deployed = this.existingState?.deployedVersion || '';
                this.nixDockerBuild.branches.val = branches;
                this.nixDockerBuild.selectedBranch.val = selectedBranch;
                this.nixDockerBuild.commits.val = commits;
                if (preserveSelection && previous && (commits.some(item => item.id === previous) || previous === deployed)) {
                    this.nixDockerBuild.selectedCommit.val = previous;
                } else if (preserveSelection && deployed && this.sourceMatchesInitial()) {
                    this.nixDockerBuild.selectedCommit.val = deployed;
                } else {
                    this.nixDockerBuild.selectedCommit.val = commits[0]?.id || '';
                }
                this.nixDockerBuild.repository.val = okStatus(
                    nixRepositoryDiscoveryKey(this.form.nixRepo.val),
                    'Repository accessible.',
                );
                this.nixDockerBuild.commitDiscovery.val = okStatus(
                    nixCommitDiscoveryKey(this.form.nixRepo.val, selectedBranch),
                    'Commits loaded.',
                );
            } else if (sourceType === SOURCE_DOCKER_IMAGE) {
                const result = response?.containerImage;
                if (!result) throw new Error('Version response did not include image tags.');
                const tags = result.tags || [];
                const previous = this.containerImage.selectedTag.val;
                const deployed = this.existingState?.deployedVersion || '';
                this.containerImage.tags.val = tags;
                if (preserveSelection && previous && (tags.some(item => item.id === previous) || previous === deployed)) {
                    this.containerImage.selectedTag.val = previous;
                } else if (preserveSelection && deployed && this.sourceMatchesInitial()) {
                    this.containerImage.selectedTag.val = deployed;
                } else {
                    this.containerImage.selectedTag.val = tags[0]?.id || '';
                }
                this.containerImage.discovery.val = okStatus(
                    imageDiscoveryKey(this.form.containerImage.val),
                    'Image accessible.',
                );
            }
            return true;
        } catch (error) {
            if (!isCurrent()) return false;
            const message = error.message || 'Failed to load deployment versions.';
            if (sourceType === SOURCE_NIX_DOCKER) {
                this.nixDockerBuild.repository.val = errorStatus(nixRepositoryDiscoveryKey(this.form.nixRepo.val), message);
                this.nixDockerBuild.commitDiscovery.val = errorStatus(key, message);
                this.nixDockerBuild.branches.val = [];
                this.nixDockerBuild.commits.val = [];
            } else if (sourceType === SOURCE_DOCKER_IMAGE) {
                this.containerImage.discovery.val = errorStatus(imageDiscoveryKey(this.form.containerImage.val), message);
                this.containerImage.tags.val = [];
            }
            return false;
        } finally {
            this.updateVersionActivity();
        }
    }

    async loadVersions({branch = '', preserveSelection = true, refreshAvailableBranches = true} = {}) {
        if (this.form.sourceType.val === SOURCE_DOCKER_IMAGE) {
            if (imageVersionFromReference(this.form.containerImage.val)) return true;
            return this.discoverImageVersions({preserveSelection});
        }
        if (this.form.sourceType.val !== SOURCE_NIX_DOCKER) return false;
        const requestedBranch = branch || this.nixDockerBuild.selectedBranch.val;
        if (requestedBranch && this.repositoryStatus().status === 'ok' && !refreshAvailableBranches) {
            this.nixDockerBuild.selectedBranch.val = requestedBranch;
            const loaded = await this.discoverCommits(requestedBranch, {preserveSelection});
            if (loaded && this.desiredRunning.val) await this.validateExactNixSelection();
            return loaded;
        }
        const repositoryReady = await this.discoverRepository({refresh: refreshAvailableBranches});
        if (!repositoryReady) return false;
        const selectedBranch = this.nixDockerBuild.selectedBranch.val;
        const loaded = selectedBranch ? await this.discoverCommits(selectedBranch, {preserveSelection}) : true;
        if (loaded && this.desiredRunning.val) await this.validateExactNixSelection();
        return loaded;
    }

    setDesiredRunning(running) {
        this.desiredRunning.val = Boolean(running);
        if (this.desiredRunning.val && this.form.sourceType.val === SOURCE_NIX_DOCKER) {
            void this.validateExactNixSelection();
        } else {
            this.invalidateExactValidation();
        }
    }

    selectedTargetVersion() {
        if (this.form.sourceType.val === SOURCE_DOCKER_IMAGE) {
            return imageVersionFromReference(this.form.containerImage.val) || this.containerImage.selectedTag.val.trim();
        }
        if (this.form.sourceType.val === SOURCE_NIX_DOCKER) return this.nixDockerBuild.selectedCommit.val.trim();
        return '';
    }

    sourceMatchesInitial() {
        const current = this.persistedSource();
        return current.type === this.initialSource.type
            && current.repo === this.initialSource.repo
            && current.flake === this.initialSource.flake
            && current.image === this.initialSource.image;
    }

    initialRepositoryTrusted() {
        return this.mode === 'update'
            && Boolean(this.existingState)
            && this.form.sourceType.val === SOURCE_NIX_DOCKER
            && this.initialSource.type === SOURCE_NIX_DOCKER
            && this.form.nixRepo.val.trim() === this.initialSource.repo;
    }

    initialImageTrusted() {
        return Boolean(this.existingState?.desiredRunning)
            && this.form.sourceType.val === SOURCE_DOCKER_IMAGE
            && this.initialSource.type === SOURCE_DOCKER_IMAGE
            && this.form.containerImage.val.trim() === this.initialSource.image;
    }

    initialNixSourceTrusted() {
        return this.initialRepositoryTrusted()
            && this.form.nixFlake.val.trim() === this.initialSource.flake;
    }

    createDesiredVersion() {
        return this.selectedTargetVersion()
            || (this.existingState?.deployedVersion && this.sourceMatchesInitial() ? this.existingState.deployedVersion : '');
    }

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
        const previousSource = this.persistedSource();
        const identity = document?.identity || {};
        const spec = document?.spec || {};
        const workload = spec.container1Spec || spec.systemdSpec || {};
        replaceDeploymentFormFromConfig(this.form, {
            id: Number(this.form.deploymentId.val || 0),
            name: identity.name || '',
            spaceId: Number(identity.spaceId || 0),
            nodeId: Number(document?.nodeId || 0),
            spec,
        });
        const nextSource = this.persistedSource();
        const sourceChanged = previousSource.type !== nextSource.type
            || previousSource.repo !== nextSource.repo
            || previousSource.image !== nextSource.image;
        if (sourceChanged) {
            this.cancel('versions');
            this.cancel('repository');
            this.cancel('commits');
            this.cancel('image');
            this.nixDockerBuild.repository.val = idleStatus();
            this.nixDockerBuild.commitDiscovery.val = idleStatus();
            this.nixDockerBuild.branches.val = [];
            this.nixDockerBuild.commits.val = [];
            this.nixDockerBuild.selectedBranch.val = '';
            this.containerImage.discovery.val = idleStatus();
            this.containerImage.tags.val = [];
        }
        this.invalidateExactValidation();
        this.desiredRunning.val = Boolean(workload.running);
        const version = (workload.version || '').trim();
        if (this.form.sourceType.val === SOURCE_DOCKER_IMAGE) {
            this.containerImage.selectedTag.val = version;
        } else if (this.form.sourceType.val === SOURCE_NIX_DOCKER) {
            this.nixDockerBuild.selectedCommit.val = version;
        }
        this.documentRevision.val += 1;
        this.updateVersionActivity();
        if (this.desiredRunning.val && this.form.sourceType.val === SOURCE_NIX_DOCKER) {
            void this.validateExactNixSelection();
        }
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

    // toMovePayload returns a DeploymentSpaceMoveRequest when the form names a
    // different space than the deployment currently has, else null. Moves are
    // an independent operation guarded by the deployment's space version.
    toMovePayload() {
        if (!this.existingState) throw new Error('Cannot produce move payload without existing deployment state');
        const nextSpaceId = Number(this.form.spaceId.val || 0);
        if (!nextSpaceId || nextSpaceId === Number(this.existingState.spaceId || 0)) return null;
        return {
            deploymentId: this.existingState.id,
            spaceId: nextSpaceId,
            spaceVersion: Number(this.existingState.spaceVersion || 0) + 1,
        };
    }

    toUpdatePayload() {
        if (!this.existingState) throw new Error('Cannot produce update payload without existing deployment state');
        const payload = {
            deploymentId: this.existingState.id,
            version: this.existingState.currentVersion + 1,
        };
        const nextSpec = formToSpec(this.form);
        if (JSON.stringify(nextSpec) !== this.initialSpecKey) {
            payload.spec = formToSpec(this.form, {
                version: this.createDesiredVersion(),
                running: Boolean(this.desiredRunning.val),
            });
        }
        const targetVersion = this.selectedTargetVersion();
        if (!this.desiredRunning.val && this.existingState.desiredRunning) {
            payload.stop = true;
        } else if (this.desiredRunning.val && targetVersion
            && (targetVersion !== (this.existingState.deployedVersion || '') || !this.existingState.desiredRunning)) {
            payload.targetVersion = targetVersion;
        }
        return payload;
    }
}
