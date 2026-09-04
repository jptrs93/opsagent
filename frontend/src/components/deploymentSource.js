export const SOURCE_NIX_DOCKER = 'nixDockerBuild';
export const SOURCE_DOCKER_IMAGE = 'containerImage';

export const FULL_GIT_COMMIT_RE = /^[0-9a-f]{40}$/i;

const key = values => JSON.stringify(values);
const clean = value => (value || '').trim();

export const nixRepositoryDiscoveryKey = repo => key(['nix-repository', clean(repo)]);
export const nixCommitDiscoveryKey = (repo, branch) => key(['nix-commits', clean(repo), clean(branch)]);
export const nixExactValidationKey = (repo, commit, flakePath) => key([
    'nix-exact',
    clean(repo),
    clean(commit),
    clean(flakePath),
]);
export const imageDiscoveryKey = image => key(['container-image', clean(image)]);

export function buildNixRepositoryDiscoveryRequest(repo, {refresh = true} = {}) {
    return {nixDockerBuild: {
        repoUrl: clean(repo),
        selectedBranch: '',
        selectedCommit: undefined,
        selectedFlakePath: '',
        refreshAvailableBranches: Boolean(refresh),
        refreshAvailableCommits: false,
        checkRepo: true,
        checkBranch: false,
        checkCommit: false,
        checkFlakePath: false,
    }};
}

export function buildNixCommitDiscoveryRequest(repo, branch, {refresh = true} = {}) {
    return {nixDockerBuild: {
        repoUrl: clean(repo),
        selectedBranch: clean(branch),
        selectedCommit: undefined,
        selectedFlakePath: '',
        refreshAvailableBranches: false,
        refreshAvailableCommits: Boolean(refresh),
        checkRepo: true,
        checkBranch: true,
        checkCommit: false,
        checkFlakePath: false,
    }};
}

// One request for a known branch: repository access, the branch list, the
// branch check, and that branch's commits.
export function buildNixListingRequest(repo, branch, {refresh = true} = {}) {
    return {nixDockerBuild: {
        repoUrl: clean(repo),
        selectedBranch: clean(branch),
        selectedCommit: undefined,
        selectedFlakePath: '',
        refreshAvailableBranches: Boolean(refresh),
        refreshAvailableCommits: Boolean(refresh),
        checkRepo: true,
        checkBranch: true,
        checkCommit: false,
        checkFlakePath: false,
    }};
}

export function buildExactNixValidationRequest(repo, commit, flakePath) {
    return {nixDockerBuild: {
        repoUrl: clean(repo),
        selectedBranch: '',
        selectedCommit: {id: clean(commit)},
        selectedFlakePath: clean(flakePath),
        refreshAvailableBranches: false,
        refreshAvailableCommits: false,
        checkRepo: true,
        checkBranch: false,
        checkCommit: true,
        checkFlakePath: true,
    }};
}

export function buildImageDiscoveryRequest(image, {refresh = true} = {}) {
    return {containerImage: {image: clean(image), refreshVersions: Boolean(refresh)}};
}

export function attestNixRepositoryResponse(response, repo) {
    const result = response?.nixDockerBuild;
    return result
        && result.checkedRepoUrl === clean(repo)
        && result.gitRepository?.checked
        ? result
        : null;
}

export function attestNixCommitDiscoveryResponse(response, repo, branch) {
    const result = attestNixRepositoryResponse(response, repo);
    const expectedBranch = clean(branch);
    return result
        && result.checkedBranch === expectedBranch
        && result.branchCheck?.checked
        && result.availableCommits?.branch === expectedBranch
        ? result
        : null;
}

export function attestExactNixValidationResponse(response, repo, commit, flakePath) {
    const result = attestNixRepositoryResponse(response, repo);
    return result
        && result.checkedCommit?.id === clean(commit)
        && result.commitCheck?.checked
        && result.checkedFlakePath === clean(flakePath)
        && result.nixFlakeFile?.checked
        ? result
        : null;
}

export function attestImageDiscoveryResponse(response) {
    const result = response?.containerImage;
    return result?.image?.checked ? result : null;
}

export function imageVersionFromReference(raw) {
    let image = clean(raw);
    image = image.replace(/^docker:\/\//, '').replace(/^https?:\/\//, '').replace(/\/$/, '');
    const digestIdx = image.indexOf('@');
    if (digestIdx >= 0) return image.slice(digestIdx + 1);
    const lastSlash = image.lastIndexOf('/');
    const lastColon = image.lastIndexOf(':');
    if (lastColon > lastSlash) return image.slice(lastColon + 1);
    return '';
}

export function validateLocalFlakePath(raw) {
    const path = clean(raw);
    if (!path) return {ok: false, message: 'Flake path is required.'};
    if (path.startsWith('/') || path.startsWith('\\') || /^[a-z]:[\\/]/i.test(path)) {
        return {ok: false, message: 'Flake path must be relative to the repository.'};
    }
    const parts = path.split('/');
    if (parts.some(part => part === '..')) {
        return {ok: false, message: 'Flake path must remain inside the repository.'};
    }
    if (parts.at(-1) !== 'flake.nix') {
        return {ok: false, message: 'Flake path must end in flake.nix.'};
    }
    return {ok: true, message: ''};
}
