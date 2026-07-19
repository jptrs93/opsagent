import assert from "node:assert/strict";
import {DeploymentCreationUpdate} from "../../src/components/deploymentCreationUpdate.js";
import {formInvalidReason} from "../../src/components/deploymentForm.js";
import {
    buildExactNixValidationRequest,
    FULL_GIT_COMMIT_RE,
    imageDiscoveryKey,
    nixCommitDiscoveryKey,
    nixExactValidationKey,
    nixRepositoryDiscoveryKey,
    SOURCE_DOCKER_IMAGE,
    SOURCE_NIX_DOCKER,
    validateLocalFlakePath,
} from "../../src/components/deploymentSource.js";
import {fixturePresets} from "./mockData.js";

const COMMIT_A = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa';
const COMMIT_B = 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb';
const REPO = 'github.com/acme/platform';
const FLAKE = 'services/api/flake.nix';

assert.equal(nixRepositoryDiscoveryKey(' repo '), JSON.stringify(['nix-repository', 'repo']));
assert.equal(nixCommitDiscoveryKey('repo', ' main '), JSON.stringify(['nix-commits', 'repo', 'main']));
assert.equal(nixExactValidationKey('repo', COMMIT_A, 'flake.nix'), JSON.stringify(['nix-exact', 'repo', COMMIT_A, 'flake.nix']));
assert.equal(imageDiscoveryKey(' postgres '), JSON.stringify(['container-image', 'postgres']));
assert.ok(FULL_GIT_COMMIT_RE.test(COMMIT_A));
assert.equal(buildExactNixValidationRequest(REPO, COMMIT_A, FLAKE).nixDockerBuild.selectedBranch, '');

assert.deepEqual(validateLocalFlakePath('flake.nix'), {ok: true, message: ''});
assert.equal(validateLocalFlakePath('../flake.nix').ok, false);
assert.equal(validateLocalFlakePath('/tmp/flake.nix').ok, false);
assert.equal(validateLocalFlakePath('services/api/default.nix').ok, false);

const deferred = () => {
    let resolve;
    const promise = new Promise(done => { resolve = done; });
    return {promise, resolve};
};

const repositoryResponse = (repo, branches = ['main']) => ({
    nixDockerBuild: {
        checkedRepoUrl: repo,
        gitRepository: {checked: true, ok: true, message: 'Repository accessible.'},
        availableBranches: {loaded: true, branches},
    },
});

const exactResponse = (repo, commit, flakePath, ok = true) => ({
    nixDockerBuild: {
        checkedRepoUrl: repo,
        gitRepository: {checked: true, ok: true, message: 'Repository accessible.'},
        checkedCommit: {id: commit},
        commitCheck: {checked: true, ok, message: ok ? 'Commit exists.' : 'Commit missing.'},
        checkedFlakePath: flakePath,
        nixFlakeFile: {checked: true, ok, message: ok ? 'Regular flake file.' : 'Flake missing.'},
    },
});

function configuredNixModel(validateSource, running = false) {
    const model = new DeploymentCreationUpdate({validateSource});
    model.form.name.val = 'api';
    model.form.nodeId.val = 1;
    model.form.sourceType.val = SOURCE_NIX_DOCKER;
    model.form.nixRepo.val = REPO;
    model.form.nixFlake.val = FLAKE;
    model.nixDockerBuild.selectedCommit.val = COMMIT_A;
    model.desiredRunning.val = running;
    return model;
}

// A response for an earlier repository key cannot populate current discovery state.
const firstRepo = deferred();
const secondRepo = deferred();
let repoCall = 0;
const staleModel = configuredNixModel(() => (++repoCall === 1 ? firstRepo.promise : secondRepo.promise));
const firstDiscovery = staleModel.discoverRepository();
staleModel.form.nixRepo.val = 'github.com/acme/other';
staleModel.onRepositoryInput();
const secondDiscovery = staleModel.discoverRepository();
firstRepo.resolve(repositoryResponse(REPO, ['stale']));
await firstDiscovery;
assert.deepEqual(staleModel.nixDockerBuild.branches.val, []);
secondRepo.resolve(repositoryResponse('github.com/acme/other', ['main']));
await secondDiscovery;
assert.deepEqual(staleModel.nixDockerBuild.branches.val, ['main']);

// Changing the selected commit immediately invalidates an exact attestation.
const exactRequests = [];
const exactModel = configuredNixModel(request => {
    const pending = deferred();
    exactRequests.push({request, pending});
    return pending.promise;
}, true);
const validationA = exactModel.validateExactNixSelection();
exactRequests[0].pending.resolve(exactResponse(REPO, COMMIT_A, FLAKE));
await validationA;
assert.equal(exactModel.hasCurrentExactNixValidation(), true);
exactModel.selectCommit(COMMIT_B);
assert.equal(exactModel.hasCurrentExactNixValidation(), false);
assert.equal(exactModel.nixDockerBuild.exactValidation.val.status, 'checking');
exactRequests[1].pending.resolve(exactResponse(REPO, COMMIT_B, FLAKE));
await exactRequests[1].pending.promise;
await new Promise(resolve => setTimeout(resolve, 0));
assert.equal(exactModel.hasCurrentExactNixValidation(), true);

// Stopped Nix is structural-only; Running requires current exact validation.
const stoppedModel = configuredNixModel(async () => exactResponse(REPO, COMMIT_A, FLAKE), false);
assert.equal(formInvalidReason(stoppedModel.form, {nodeOptions: [{id: 1}]}), '');
assert.equal(stoppedModel.runningNixInvalidReason(), '');
stoppedModel.desiredRunning.val = true;
assert.match(stoppedModel.runningNixInvalidReason(), /Validate/);
await stoppedModel.validateExactNixSelection();
assert.equal(stoppedModel.runningNixInvalidReason(), '');
stoppedModel.desiredRunning.val = false;
stoppedModel.nixDockerBuild.selectedCommit.val = 'main';
assert.match(stoppedModel.runningNixInvalidReason(), /full 40-character/);

// HCL replacement selects a version without manufacturing branch, commit, or trust data.
const hclModel = configuredNixModel(async () => ({}), false);
hclModel.nixDockerBuild.selectedBranch.val = 'main';
hclModel.nixDockerBuild.branches.val = ['main'];
hclModel.nixDockerBuild.commits.val = [{id: COMMIT_A, label: 'Discovered'}];
const hclDocument = hclModel.toDocument();
hclDocument.desiredState.version = COMMIT_B;
hclModel.replaceDocument(hclDocument);
assert.equal(hclModel.nixDockerBuild.selectedCommit.val, COMMIT_B);
assert.deepEqual(hclModel.nixDockerBuild.branches.val, ['main']);
assert.deepEqual(hclModel.nixDockerBuild.commits.val, [{id: COMMIT_A, label: 'Discovered'}]);
assert.equal(hclModel.nixDockerBuild.exactValidation.val.status, 'idle');

// Docker keeps explicit versions and does not inherit Nix's exact-validation policy.
const dockerModel = new DeploymentCreationUpdate({validateSource: async () => ({})});
dockerModel.form.sourceType.val = SOURCE_DOCKER_IMAGE;
dockerModel.form.containerImage.val = 'ghcr.io/acme/api:v3';
dockerModel.desiredRunning.val = true;
assert.equal(dockerModel.createDesiredVersion(), 'v3');
assert.equal(dockerModel.runningNixInvalidReason(), '');

const invalidImageModel = new DeploymentCreationUpdate({validateSource: async () => ({containerImage: {
    image: {checked: true, ok: false, message: 'invalid image'},
}})});
invalidImageModel.form.sourceType.val = SOURCE_DOCKER_IMAGE;
invalidImageModel.form.containerImage.val = 'not an image';
await invalidImageModel.discoverImageVersions();
assert.equal(invalidImageModel.sourcePathInvalidReason(), 'Image path invalid.');

const invalidRepoModel = configuredNixModel(async () => ({nixDockerBuild: {
    checkedRepoUrl: REPO,
    gitRepository: {checked: true, ok: false, message: 'invalid repository'},
    availableBranches: {loaded: true, branches: []},
}}));
await invalidRepoModel.discoverRepository();
assert.equal(invalidRepoModel.sourcePathInvalidReason(), 'Nix repository path invalid.');

const duplicateNameModel = new DeploymentCreationUpdate({validateSource: async () => ({})});
duplicateNameModel.form.name.val = 'api';
duplicateNameModel.form.spaceId.val = 1;
assert.equal(formInvalidReason(duplicateNameModel.form, {
    deployments: [{config: {id: 10, configId: {name: 'api', spaceId: 1}}}],
}), 'Deployment name is unavailable in this space.');
duplicateNameModel.form.deploymentId.val = 10;
assert.notEqual(formInvalidReason(duplicateNameModel.form, {
    deployments: [{config: {id: 10, configId: {name: 'api', spaceId: 1}}}],
}), 'Deployment name is unavailable in this space.');

// A persisted running source remains trusted across failed discovery refreshes.
const runningImagePreset = fixturePresets.updateContainer;
const trustedImageModel = new DeploymentCreationUpdate({
    deployment: runningImagePreset.deployment,
    deploymentConfig: runningImagePreset.deploymentConfig,
    validateSource: async () => { throw new Error('Fixture source validation failure'); },
});
await trustedImageModel.discoverImageVersions();
assert.equal(trustedImageModel.imageStatus().status, 'ok');
assert.equal(trustedImageModel.sourcePathInvalidReason(), '');
trustedImageModel.form.containerImage.val = 'ghcr.io/acme/changed';
trustedImageModel.onImageInput();
await trustedImageModel.discoverImageVersions();
assert.equal(trustedImageModel.imageStatus().status, 'error');
assert.equal(trustedImageModel.sourcePathInvalidReason(), 'Image path invalid.');

const runningNixPreset = fixturePresets.updateNixStopped;
const trustedNixModel = new DeploymentCreationUpdate({
    deployment: {...runningNixPreset.deployment, desiredRunning: true},
    deploymentConfig: runningNixPreset.deploymentConfig,
    validateSource: async () => { throw new Error('Fixture source validation failure'); },
});
assert.equal(trustedNixModel.repositoryStatus().status, 'ok');
assert.equal(trustedNixModel.runningNixInvalidReason(), '');
await trustedNixModel.discoverRepository();
assert.equal(trustedNixModel.repositoryStatus().status, 'ok');
assert.equal(trustedNixModel.sourcePathInvalidReason(), '');
trustedNixModel.form.nixRepo.val = 'github.com/acme/changed';
trustedNixModel.onRepositoryInput();
await trustedNixModel.discoverRepository();
assert.equal(trustedNixModel.repositoryStatus().status, 'error');
assert.equal(trustedNixModel.sourcePathInvalidReason(), 'Nix repository path invalid.');

console.log('Deployment source state checks passed.');
