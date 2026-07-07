import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {
    buildValidateSourceRequest,
    deploymentConfigToForm,
    emptyDeploymentForm,
    formToDeploymentIdentifier,
    formToSpec,
    hasTrustedSourceValidation,
    imageVersionFromReference,
    sourceCheckFromValidation,
    sourceValidationKey,
    validationSourceResult,
} from "./deploymentForm.js";

export const SOURCE_NIX_DOCKER = 'nixDockerBuild';
export const SOURCE_DOCKER_IMAGE = 'containerImage';
export const SOURCE_GITHUB_RELEASE = 'githubRelease';

const idleValidity = () => ({status: 'idle', message: '', fieldKey: ''});
const checkingValidity = (message, fieldKey) => ({status: 'checking', message, fieldKey});

const preserveOkValidity = (previous, next) => {
    if (previous?.status === 'ok' && previous.fieldKey === next.fieldKey && next.status !== 'ok') return previous;
    return next;
};

export class DeploymentCreationUpdate {
    constructor({deployment = null, deploymentConfig = null} = {}) {
        this.existingState = deployment;
        this.form = deploymentConfig ? deploymentConfigToForm(deploymentConfig) : emptyDeploymentForm();
        this.form.deploymentCreationUpdate = this;
        this.desiredRunning = van.state(deployment ? Boolean(deployment.desiredRunning) : true);
        this.initialSpecKey = JSON.stringify(formToSpec(this.form));
        this.initialSpaceId = Number(this.form.spaceId.val || 0);
        this.initialSourceKey = this.sourceKey();
        const hasExistingNixVersion = deployment?.variant === SOURCE_NIX_DOCKER && Boolean(deployment.deployedVersion);
        const hasExistingImageVersion = deployment?.variant === SOURCE_DOCKER_IMAGE && Boolean(imageVersionFromReference(this.form.containerImage.val) || deployment.deployedVersion);

        this.nixDockerBuild = {
            selectedBranch: van.state(''),
            selectedCommit: van.state(deployment?.variant === SOURCE_NIX_DOCKER ? (deployment.deployedVersion || '') : ''),
            selectedCommitSourceKey: van.state(hasExistingNixVersion ? this.initialSourceKey : ''),
            branches: van.state([]),
            commits: van.state([]),
        };
        this.containerImage = {
            selectedTag: van.state(deployment?.variant === SOURCE_DOCKER_IMAGE ? (imageVersionFromReference(this.form.containerImage.val) || deployment.deployedVersion || '') : ''),
            selectedTagSourceKey: van.state(hasExistingImageVersion ? this.initialSourceKey : ''),
            tags: van.state(deployment?.variant === SOURCE_DOCKER_IMAGE && deployment.deployedVersion
                ? [{id: deployment.deployedVersion, label: 'Current'}]
                : []),
        };
        this.githubRelease = {
            selectedRelease: van.state(deployment?.variant === SOURCE_GITHUB_RELEASE ? (deployment.deployedVersion || '') : ''),
            releases: van.state([]),
        };

        /** @type {Map<string, Map<string, Array>>} */
        this.cachedRepoBranchCommitOptions = new Map();
        this.cachedRepoBranchCommitOptionsVersion = van.state(0);

        this.repoValid = van.state(idleValidity());
        this.flakePathValid = van.state(idleValidity());
        this.imageValid = van.state(idleValidity());
        this.updateValidityFromCheck(this.form.repoCheck.val);
    }

    sourceKey() {
        return sourceValidationKey(this.form);
    }

    repoValidityKey(sourceType = this.form.sourceType.val, repo = this.currentSourceID()) {
        return `${sourceType}:${(repo || '').trim()}`;
    }

    flakeValidityKey(repo = this.form.nixRepo.val, flake = this.form.nixFlake.val) {
        return `${SOURCE_NIX_DOCKER}:${(repo || '').trim()}:${(flake || '').trim()}`;
    }

    currentSourceID() {
        if (this.form.sourceType.val === SOURCE_DOCKER_IMAGE) return this.form.containerImage.val.trim();
        return this.form.nixRepo.val.trim();
    }

    activeRepoCheck() {
        const c = this.form.repoCheck.val;
        const repo = this.currentSourceID();
        if (!repo || c.sourceType !== this.form.sourceType.val || c.repo !== repo || c.sourceKey !== this.sourceKey()) {
            return {status: repo ? 'idle' : 'empty', message: ''};
        }
        return c;
    }

    updateCachedRepoBranchCommitOptions(validateResult) {
        const sourceResult = validateResult?.nixDockerBuild;
        if (!sourceResult) return;
        const repoName = this.form.nixRepo.val.trim();
        if (!repoName) return;
        const branchMap = new Map(this.cachedRepoBranchCommitOptions.get(repoName) || []);
        for (const branch of sourceResult.availableBranches?.branches || []) {
            if (!branchMap.has(branch)) branchMap.set(branch, []);
        }
        const branch = sourceResult.availableCommits?.branch || '';
        const commits = sourceResult.availableCommits?.commits || [];
        if (branch || commits.length > 0) {
            branchMap.set(branch, commits);
        }
        this.cachedRepoBranchCommitOptions.set(repoName, branchMap);
        this.cachedRepoBranchCommitOptionsVersion.val += 1;
    }

    branchCommitOptions(repoName = this.form.nixRepo.val.trim()) {
        this.cachedRepoBranchCommitOptionsVersion.val;
        return this.cachedRepoBranchCommitOptions.get(repoName) || new Map();
    }

    setRepoCheckChecking(message = 'Checking repository access...') {
        const sourceType = this.form.sourceType.val;
        const repo = this.currentSourceID();
        const sourceKey = this.sourceKey();
        this.form.repoCheck.val = {...(this.form.repoCheck.val || {}), status: 'checking', message, repo, sourceType, sourceKey};
        const repoKey = this.repoValidityKey(sourceType, repo);
        if (sourceType === SOURCE_DOCKER_IMAGE) {
            if (!(this.imageValid.val.status === 'ok' && this.imageValid.val.fieldKey === repoKey)) {
                this.imageValid.val = checkingValidity(message, repoKey);
            }
        } else {
            if (!(this.repoValid.val.status === 'ok' && this.repoValid.val.fieldKey === repoKey)) {
                this.repoValid.val = checkingValidity(message, repoKey);
            }
            const flakeKey = this.flakeValidityKey(repo, this.form.nixFlake.val);
            if (this.form.nixFlake.val.trim() && !(this.flakePathValid.val.status === 'ok' && this.flakePathValid.val.fieldKey === flakeKey)) {
                this.flakePathValid.val = checkingValidity('Checking path...', flakeKey);
            }
        }
    }

    setRepoCheckError(message) {
        const sourceType = this.form.sourceType.val;
        const repo = this.currentSourceID();
        const sourceKey = this.sourceKey();
        this.form.repoCheck.val = {...(this.form.repoCheck.val || {}), status: 'error', message, repo, sourceType, sourceKey};
        this.updateValidityFromCheck(this.form.repoCheck.val);
    }

    setRepoCheckFromValidation(validateResult, repo = this.currentSourceID(), sourceType = this.form.sourceType.val, sourceKey = this.sourceKey(), opts = {}) {
        this.form.repoCheck.val = sourceCheckFromValidation(this.form, validateResult, repo, sourceType, sourceKey);
        this.updateCachedRepoBranchCommitOptions(validateResult);
        this.updateValidityFromCheck(this.form.repoCheck.val);
        if (opts.syncVersionOptions !== false) this.syncVersionOptionsFromCheck(this.form.repoCheck.val);
    }

    updateValidityFromCheck(check) {
        const sourceType = check?.sourceType || this.form.sourceType.val;
        if (!check || check.status === 'idle') {
            this.repoValid.val = idleValidity();
            this.flakePathValid.val = idleValidity();
            this.imageValid.val = idleValidity();
            return;
        }
        if (sourceType === SOURCE_DOCKER_IMAGE) {
            const image = check.image || {};
            const fieldKey = this.repoValidityKey(sourceType, check.repo);
            this.imageValid.val = preserveOkValidity(this.imageValid.val, {
                status: check.status === 'checking' ? 'checking' : (image.ok ? 'ok' : 'error'),
                message: image.message || check.message || '',
                fieldKey,
            });
            return;
        }
        const gitRepository = check.gitRepository || {};
        const repoKey = this.repoValidityKey(sourceType, check.repo);
        this.repoValid.val = preserveOkValidity(this.repoValid.val, {
            status: check.status === 'checking' ? 'checking' : (gitRepository.ok ? 'ok' : 'error'),
            message: gitRepository.message || check.message || '',
            fieldKey: repoKey,
        });
        const flakePath = this.form.nixFlake.val.trim();
        if (!flakePath) {
            this.flakePathValid.val = idleValidity();
            return;
        }
        const nixFlakeFile = check.nixFlakeFile || {};
        const flakeKey = this.flakeValidityKey(check.repo, flakePath);
        this.flakePathValid.val = preserveOkValidity(this.flakePathValid.val, {
            status: check.status === 'checking' ? 'checking' : (nixFlakeFile.ok ? 'ok' : (nixFlakeFile.message ? 'error' : 'idle')),
            message: nixFlakeFile.message || (nixFlakeFile.ok ? 'Path verified' : ''),
            fieldKey: flakeKey,
        });
    }

    syncVersionOptionsFromCheck(check, {preserveSelection = true} = {}) {
        if (!check || check.status !== 'ok' || check.sourceKey !== this.sourceKey()) return;
        if (check.sourceType === SOURCE_DOCKER_IMAGE) {
            const tags = check.tags || [];
            this.containerImage.tags.val = tags;
            if (!preserveSelection || this.containerImage.selectedTagSourceKey.val !== check.sourceKey || !tags.some(v => v.id === this.containerImage.selectedTag.val)) {
                this.containerImage.selectedTag.val = tags[0]?.id || '';
                this.containerImage.selectedTagSourceKey.val = this.containerImage.selectedTag.val ? check.sourceKey : '';
            }
            return;
        }
        if (check.sourceType !== SOURCE_NIX_DOCKER) return;
        const branch = this.currentBranch(check, this.nixDockerBuild.selectedBranch.val);
        const commits = this.commitsForBranch(branch, check);
        this.nixDockerBuild.branches.val = check.branches || [];
        this.nixDockerBuild.commits.val = commits;
        this.nixDockerBuild.selectedBranch.val = branch;
        if (!preserveSelection || this.nixDockerBuild.selectedCommitSourceKey.val !== check.sourceKey || !commits.some(v => v.id === this.nixDockerBuild.selectedCommit.val)) {
            this.nixDockerBuild.selectedCommit.val = commits[0]?.id || '';
            this.nixDockerBuild.selectedCommitSourceKey.val = this.nixDockerBuild.selectedCommit.val ? check.sourceKey : '';
        }
    }

    currentBranch(check = this.activeRepoCheck(), selectedBranch = this.nixDockerBuild.selectedBranch.val) {
        if (check.status !== 'ok') return '';
        if (selectedBranch && (check.commitsByBranch || {})[selectedBranch]) return selectedBranch;
        const cached = this.branchCommitOptions();
        if (selectedBranch && cached.has(selectedBranch)) return selectedBranch;
        const keys = Object.keys(check.commitsByBranch || {});
        if (keys.length > 0) return keys[0];
        const branches = check.branches || [];
        if (branches.includes('main')) return 'main';
        return branches[0] || '';
    }

    commitsForBranch(branch, check = this.activeRepoCheck()) {
        if (!branch) return [];
        const cached = this.branchCommitOptions().get(branch);
        if (cached) return cached;
        return (check.commitsByBranch || {})[branch] || [];
    }

    async validateRepo() {
        const sourceType = this.form.sourceType.val;
        const repo = this.currentSourceID();
        if (!repo) {
            this.form.repoCheck.val = {status: 'idle', message: '', repo: '', sourceType, sourceKey: ''};
            this.updateValidityFromCheck(this.form.repoCheck.val);
            return;
        }
        const sourceKey = this.sourceKey();
        const c = this.form.repoCheck.val;
        if (c.sourceKey === sourceKey && (c.status === 'ok' || c.status === 'error')) return;
        this.setRepoCheckChecking('Checking repository access...');
        try {
            const req = buildValidateSourceRequest(this.form);
            const res = await capi.postV1RepoValidate(req);
            console.log('[opendeploy] repo validate response', {request: req, response: res});
            this.setRepoCheckFromValidation(res, repo, sourceType, sourceKey);
        } catch (e) {
            console.error('[opendeploy] repo validate failed', {error: e, stack: e?.stack});
            this.setRepoCheckError(e.message || 'Validation failed.');
        }
    }

    async validateSelectedCommit(branch, commit) {
        if (this.form.sourceType.val !== SOURCE_NIX_DOCKER) return;
        const selectedCommit = (commit || '').trim();
        if (!this.form.nixRepo.val.trim() || !selectedCommit) return;
        const sourceKey = this.sourceKey();
        this.setRepoCheckChecking(this.form.repoCheck.val.message || 'Checking repository access...');
        try {
            const req = buildValidateSourceRequest(this.form, {
                branch,
                commit: selectedCommit,
                checkCommit: true,
                checkFlakePath: Boolean(this.form.nixFlake.val.trim()),
            });
            const res = await capi.postV1RepoValidate(req);
            console.log('[opendeploy] repo validate selected commit response', {request: req, response: res});
            this.setRepoCheckFromValidation(res, this.form.nixRepo.val.trim(), SOURCE_NIX_DOCKER, sourceKey);
        } catch (e) {
            console.error('[opendeploy] repo validate selected commit failed', {error: e, stack: e?.stack});
            this.setRepoCheckError(e.message || 'Validation failed.');
        }
    }

    validationSourceResult(validateResult) {
        return validationSourceResult(this.form, validateResult);
    }

    hasTrustedSourceValidation() {
        return hasTrustedSourceValidation(this.form);
    }

    selectedTargetVersion() {
        if (this.form.sourceType.val === SOURCE_DOCKER_IMAGE) {
            const explicitVersion = imageVersionFromReference(this.form.containerImage.val);
            if (explicitVersion) return explicitVersion;
            const tag = this.containerImage.selectedTag.val.trim();
            if (!tag || this.containerImage.selectedTagSourceKey.val !== this.sourceKey()) return '';
            return this.containerImage.tags.val.some(v => v.id === tag) ? tag : '';
        }
        if (this.form.sourceType.val === SOURCE_NIX_DOCKER) {
            const commit = this.nixDockerBuild.selectedCommit.val.trim();
            if (!commit || this.nixDockerBuild.selectedCommitSourceKey.val !== this.sourceKey()) return '';
            if (this.commitsForBranch(this.nixDockerBuild.selectedBranch.val).some(v => v.id === commit)) return commit;
            return commit === (this.existingState?.deployedVersion || '') ? commit : '';
        }
        return this.githubRelease.selectedRelease.val.trim();
    }

    createDesiredVersion() {
        return this.selectedTargetVersion()
            || (this.existingState?.deployedVersion && this.sourceKey() === this.initialSourceKey ? this.existingState.deployedVersion : '');
    }

    toCreatePayload() {
        const targetVersion = this.createDesiredVersion();
        return {
            configId: formToDeploymentIdentifier(this.form),
            spec: formToSpec(this.form),
            desiredState: {
                version: targetVersion,
                running: this.desiredRunning.val,
            },
        };
    }

    toUpdatePayload({internalGithubRelease = false, versionOnly = false} = {}) {
        if (!this.existingState) throw new Error('Cannot produce update payload without existing deployment state');
        const payload = {
            deploymentId: this.existingState.id,
            version: this.existingState.currentVersion + 1,
        };
        if (!internalGithubRelease && !versionOnly) {
            const nextSpec = formToSpec(this.form);
            if (JSON.stringify(nextSpec) !== this.initialSpecKey) payload.spec = nextSpec;
        }
        const nextSpaceId = Number(this.form.spaceId.val || 0);
        if (!versionOnly && nextSpaceId !== this.initialSpaceId) payload.spaceId = nextSpaceId;
        const targetVersion = internalGithubRelease
            ? this.githubRelease.selectedRelease.val.trim()
            : this.selectedTargetVersion();
        if (targetVersion && (targetVersion !== (this.existingState.deployedVersion || '') || !this.existingState.desiredRunning)) {
            payload.targetVersion = targetVersion;
        }
        return payload;
    }
}
