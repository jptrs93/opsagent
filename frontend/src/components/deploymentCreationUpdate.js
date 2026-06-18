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

const idleValidity = () => ({status: 'idle', message: ''});
const checkingValidity = (message) => ({status: 'checking', message});

export class DeploymentCreationUpdate {
    constructor({deployment = null, deploymentConfig = null} = {}) {
        this.existingState = deployment;
        this.form = deploymentConfig ? deploymentConfigToForm(deploymentConfig) : emptyDeploymentForm();
        this.form.deploymentCreationUpdate = this;
        this.initialSpecKey = JSON.stringify(formToSpec(this.form));

        this.nixDockerBuild = {
            selectedBranch: van.state(''),
            selectedCommit: van.state(deployment?.variant === SOURCE_NIX_DOCKER ? (deployment.deployedVersion || '') : ''),
            selectedCommitSourceKey: van.state(''),
            branches: van.state([]),
            commits: van.state([]),
        };
        this.containerImage = {
            selectedTag: van.state(deployment?.variant === SOURCE_DOCKER_IMAGE ? (imageVersionFromReference(this.form.containerImage.val) || deployment.deployedVersion || '') : ''),
            selectedTagSourceKey: van.state(''),
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
        this.form.repoCheck.val = {status: 'checking', message, repo, sourceType, sourceKey};
        if (sourceType === SOURCE_DOCKER_IMAGE) {
            this.imageValid.val = checkingValidity(message);
        } else {
            this.repoValid.val = checkingValidity(message);
            if (this.form.nixFlake.val.trim()) this.flakePathValid.val = checkingValidity('Checking path...');
        }
    }

    setRepoCheckError(message) {
        const sourceType = this.form.sourceType.val;
        const repo = this.currentSourceID();
        const sourceKey = this.sourceKey();
        this.form.repoCheck.val = {status: 'error', message, repo, sourceType, sourceKey};
        this.updateValidityFromCheck(this.form.repoCheck.val);
    }

    setRepoCheckFromValidation(validateResult, repo = this.currentSourceID(), sourceType = this.form.sourceType.val, sourceKey = this.sourceKey()) {
        this.form.repoCheck.val = sourceCheckFromValidation(this.form, validateResult, repo, sourceType, sourceKey);
        this.updateCachedRepoBranchCommitOptions(validateResult);
        this.updateValidityFromCheck(this.form.repoCheck.val);
        this.syncVersionOptionsFromCheck(this.form.repoCheck.val);
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
            this.imageValid.val = {
                status: check.status === 'checking' ? 'checking' : (image.ok ? 'ok' : 'error'),
                message: image.message || check.message || '',
            };
            return;
        }
        const gitRepository = check.gitRepository || {};
        this.repoValid.val = {
            status: check.status === 'checking' ? 'checking' : (gitRepository.ok ? 'ok' : 'error'),
            message: gitRepository.message || check.message || '',
        };
        const flakePath = this.form.nixFlake.val.trim();
        if (!flakePath) {
            this.flakePathValid.val = idleValidity();
            return;
        }
        const nixFlakeFile = check.nixFlakeFile || {};
        this.flakePathValid.val = {
            status: check.status === 'checking' ? 'checking' : (nixFlakeFile.ok ? 'ok' : (nixFlakeFile.message ? 'error' : 'idle')),
            message: nixFlakeFile.message || (nixFlakeFile.ok ? 'Path verified' : ''),
        };
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
            return this.commitsForBranch(this.nixDockerBuild.selectedBranch.val).some(v => v.id === commit) ? commit : '';
        }
        return this.githubRelease.selectedRelease.val.trim();
    }

    toCreatePayload() {
        return {
            configId: formToDeploymentIdentifier(this.form),
            spec: formToSpec(this.form),
        };
    }

    toCreateUpdatePayload(createdConfig) {
        const targetVersion = this.selectedTargetVersion();
        if (!targetVersion || !createdConfig?.id) return null;
        return {
            deploymentId: createdConfig.id,
            targetVersion,
            version: (createdConfig.version || 0) + 1,
        };
    }

    toUpdatePayload({internalGithubRelease = false} = {}) {
        if (!this.existingState) throw new Error('Cannot produce update payload without existing deployment state');
        const payload = {
            deploymentId: this.existingState.id,
            version: this.existingState.currentVersion + 1,
        };
        if (!internalGithubRelease) {
            const nextSpec = formToSpec(this.form);
            if (JSON.stringify(nextSpec) !== this.initialSpecKey) payload.spec = nextSpec;
        }
        const targetVersion = internalGithubRelease
            ? this.githubRelease.selectedRelease.val.trim()
            : this.selectedTargetVersion();
        if (targetVersion) payload.targetVersion = targetVersion;
        return payload;
    }
}
