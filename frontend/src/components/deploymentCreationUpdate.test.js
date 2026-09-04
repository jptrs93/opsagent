import assert from "node:assert/strict";
import {test} from "node:test";
import {
    DeploymentCreationUpdate,
    localSourceLayers,
    overallSourceStatus,
    SOURCE_DOCKER_IMAGE,
    SOURCE_NIX_DOCKER,
} from "./deploymentCreationUpdate.js";

const REPO = "github.com/acme/platform";
const FLAKE = "services/web/flake.nix";
const SHA_A = "a".repeat(40);
const SHA_B = "b".repeat(40);
const SHA_C = "c".repeat(40);

const settle = () => new Promise(resolve => setTimeout(resolve, 5));

// A fake validate endpoint answering every request shape the model builds,
// recording each request so tests can assert what was asked.
function fakeValidate({branches = ["main", "dev"], commits = {main: [SHA_A, SHA_B], dev: [SHA_C]}, flakeMissingAt = [], repoError = "", tags = ["1.2.0", "1.1.0"], imageError = ""} = {}) {
    const requests = [];
    const validateSource = async request => {
        requests.push(request);
        if (request.containerImage) {
            return {containerImage: {
                image: imageError ? {checked: true, ok: false, message: imageError} : {checked: true, ok: true, message: "Image accessible."},
                tags: imageError ? [] : tags.map(id => ({id, label: id, time: new Date("2026-01-01T00:00:00Z")})),
            }};
        }
        const src = request.nixDockerBuild;
        const res = {checkedRepoUrl: src.repoUrl};
        if (repoError) {
            res.gitRepository = {checked: true, ok: false, message: repoError};
            res.availableBranches = {loaded: true, errormessage: repoError};
            if (src.checkBranch) { res.checkedBranch = src.selectedBranch; res.branchCheck = {checked: true, ok: false, message: repoError}; }
            return {nixDockerBuild: res};
        }
        res.gitRepository = {checked: true, ok: true, message: "Repo accessible."};
        if (src.refreshAvailableBranches || src.checkBranch) res.availableBranches = {loaded: true, branches};
        if (src.checkBranch) {
            res.checkedBranch = src.selectedBranch;
            res.branchCheck = branches.includes(src.selectedBranch)
                ? {checked: true, ok: true, message: "Branch exists."}
                : {checked: true, ok: false, message: `Branch '${src.selectedBranch}' doesn't exist.`};
        }
        if (src.refreshAvailableCommits) {
            res.availableCommits = {loaded: true, branch: src.selectedBranch, commits: (commits[src.selectedBranch] || []).map(id => ({id, label: `commit ${id.slice(0, 3)}`, time: new Date("2026-02-01T00:00:00Z")}))};
        }
        if (src.checkCommit && src.checkFlakePath) {
            const id = src.selectedCommit?.id || "";
            res.checkedCommit = {id};
            res.commitCheck = {checked: true, ok: true, message: "Commit exists."};
            res.checkedFlakePath = src.selectedFlakePath;
            res.nixFlakeFile = flakeMissingAt.includes(id)
                ? {checked: true, ok: false, message: `Flake path not found at ${id.slice(0, 7)}.`}
                : {checked: true, ok: true, message: `Flake path '${src.selectedFlakePath}' is a regular file.`};
        }
        return {nixDockerBuild: res};
    };
    return {validateSource, requests};
}

const nixCreate = (fake, {repo = REPO, flake = FLAKE} = {}) => {
    const model = new DeploymentCreationUpdate({mode: "create", validateSource: fake.validateSource});
    model.form.sourceType.val = SOURCE_NIX_DOCKER;
    model.form.nixRepo.val = repo;
    model.form.nixFlake.val = flake;
    return model;
};

const nixDeployment = ({version = SHA_A, running = true} = {}) => ({
    id: 7,
    version: 3,
    def: {
        name: "web",
        spaceId: 1,
        nodeId: 11,
        spec: {container1Spec: {
            version,
            running,
            source: {nixDockerBuild: {repo: REPO, flake: FLAKE, target: ""}},
        }},
    },
});
const nixRow = ({version = SHA_A, running = true} = {}) => ({
    id: 7, version: 3, spaceId: 1, name: "web", variant: SOURCE_NIX_DOCKER, deployedVersion: version, desiredRunning: running, runnerType: "container",
});

test("local layers: blank form is unvalidated, not invalid", () => {
    const layers = localSourceLayers({type: SOURCE_NIX_DOCKER, repo: "", flake: "", target: "", image: ""});
    assert.equal(layers.repo.status, "unvalidated");
    assert.equal(layers.flake.status, "unvalidated");
    assert.equal(layers.target.status, "ok");
    assert.equal(overallSourceStatus(layers), "unvalidated");
});

test("local layers: flake path and target rules are errors", () => {
    const layers = localSourceLayers({type: SOURCE_NIX_DOCKER, repo: REPO, flake: "/abs/flake.nix", target: "web", image: ""});
    assert.equal(layers.flake.status, "error");
    assert.equal(layers.target.status, "error");
    assert.equal(overallSourceStatus(layers), "error");
});

test("overall status is the union of the layers", () => {
    const ok = {status: "ok", message: ""};
    const trusted = {status: "trusted", message: ""};
    assert.equal(overallSourceStatus({a: trusted, b: trusted}), "trusted");
    assert.equal(overallSourceStatus({a: trusted, b: ok}), "ok");
    assert.equal(overallSourceStatus({a: ok, b: {status: "checking", message: ""}}), "checking");
    assert.equal(overallSourceStatus({a: ok, b: {status: "unvalidated", message: ""}}), "unvalidated");
    assert.equal(overallSourceStatus({a: {status: "error", message: ""}, b: {status: "checking", message: ""}}), "error");
    assert.equal(overallSourceStatus({}), "unvalidated");
});

test("create: typing issues no requests; validate lists branches then commits and selects nothing", async () => {
    const fake = fakeValidate();
    const model = nixCreate(fake);
    await settle();
    assert.equal(fake.requests.length, 0);
    assert.equal(model.overallStatus(), "unvalidated");
    assert.match(model.runningInvalidReason(), /Validate the source/);

    const ok = await model.validate();
    assert.equal(ok, true);
    assert.equal(fake.requests.length, 2, "repository listing, then commits of main");
    assert.equal(fake.requests[0].nixDockerBuild.refreshAvailableBranches, true);
    assert.equal(fake.requests[0].nixDockerBuild.selectedBranch, "");
    assert.equal(fake.requests[1].nixDockerBuild.selectedBranch, "main");
    assert.deepEqual(model.nixDockerBuild.branches.val, ["main", "dev"]);
    assert.equal(model.nixDockerBuild.selectedBranch.val, "main");
    assert.equal(model.nixDockerBuild.commits.val.length, 2);
    assert.equal(model.nixDockerBuild.selectedCommit.val, "");
    assert.equal(model.overallStatus(), "ok");
    assert.equal(model.versions.val.loaded, true);
    assert.match(model.runningInvalidReason(), /Select a version/);
});

test("create: selecting a commit checks the flake there; a missing flake is an error", async () => {
    const fake = fakeValidate({flakeMissingAt: [SHA_B]});
    const model = nixCreate(fake);
    await model.validate();
    model.selectVersion(SHA_A);
    await settle();
    assert.equal(fake.requests.length, 3);
    assert.equal(fake.requests[2].nixDockerBuild.checkFlakePath, true);
    assert.equal(fake.requests[2].nixDockerBuild.selectedCommit.id, SHA_A);
    assert.equal(model.layerStatus("flake").status, "ok");
    assert.equal(model.runningInvalidReason(), "");
    assert.equal(model.toCreatePayload().spec.container1Spec.version, SHA_A);

    model.selectVersion(SHA_B);
    await settle();
    assert.equal(model.layerStatus("flake").status, "error");
    assert.equal(model.overallStatus(), "error");
    assert.match(model.sourceInvalidReason(), /Flake path: Flake path not found/);
});

test("create: a source edit drops the layers and lists; a repository edit drops the selection too", async () => {
    const fake = fakeValidate();
    const model = nixCreate(fake);
    await model.validate();
    model.selectVersion(SHA_A);
    await settle();
    assert.equal(model.overallStatus(), "ok");

    model.form.nixFlake.val = "other/flake.nix";
    await settle();
    assert.equal(model.overallStatus(), "unvalidated");
    assert.equal(model.versions.val.loaded, false);
    assert.equal(model.nixDockerBuild.commits.val.length, 0);
    assert.equal(model.nixDockerBuild.selectedCommit.val, SHA_A, "flake edit keeps the commit");

    model.form.nixRepo.val = "github.com/acme/other";
    await settle();
    assert.equal(model.nixDockerBuild.selectedCommit.val, "", "repository edit clears the commit");
    assert.equal(fake.requests.length, 3, "edits issue no requests");
});

test("create: re-validate with a known branch is a single listing request", async () => {
    const fake = fakeValidate();
    const model = nixCreate(fake);
    await model.validate();
    model.form.nixFlake.val = "other/flake.nix";
    await settle();
    // The branch survives a flake edit, so one combined request lists both.
    await model.validate();
    assert.equal(fake.requests.length, 3);
    const last = fake.requests[2].nixDockerBuild;
    assert.equal(last.selectedBranch, "main");
    assert.equal(last.refreshAvailableBranches, true);
    assert.equal(last.refreshAvailableCommits, true);
    assert.equal(model.overallStatus(), "ok");
});

test("create: branch change lists that branch's commits", async () => {
    const fake = fakeValidate();
    const model = nixCreate(fake);
    await model.validate();
    await model.selectBranch("dev");
    assert.equal(model.nixDockerBuild.selectedBranch.val, "dev");
    assert.deepEqual(model.nixDockerBuild.commits.val.map(item => item.id), [SHA_C]);
    assert.equal(fake.requests.at(-1).nixDockerBuild.selectedBranch, "dev");
});

test("create: a rejected repository is an error layer and blocks saving", async () => {
    const fake = fakeValidate({repoError: "Repository is not accessible."});
    const model = nixCreate(fake);
    const ok = await model.validate();
    assert.equal(ok, false);
    assert.equal(model.layerStatus("repo").status, "error");
    assert.match(model.sourceInvalidReason(), /Repository: Repository is not accessible/);
    assert.equal(model.versions.val.error, "Repository is not accessible.");
});

test("create: validate refuses while required fields are missing", async () => {
    const fake = fakeValidate();
    const model = nixCreate(fake, {flake: ""});
    assert.equal(await model.validate(), false);
    assert.equal(fake.requests.length, 0);
    assert.equal(model.layerStatus("flake").message, "Flake path is required.");
});

test("create: stale responses are dropped when the source changes mid-flight", async () => {
    let release;
    const gate = new Promise(resolve => { release = resolve; });
    const inner = fakeValidate();
    const validateSource = async request => { await gate; return inner.validateSource(request); };
    const model = nixCreate({validateSource});
    const pending = model.validate();
    model.form.nixRepo.val = "github.com/acme/other";
    await settle();
    release();
    assert.equal(await pending, false);
    assert.equal(model.overallStatus(), "unvalidated");
    assert.equal(model.versions.val.loaded, false);
});

test("update: an unchanged source is trusted and needs no request to save running", async () => {
    const fake = fakeValidate();
    const model = new DeploymentCreationUpdate({mode: "update", deploymentRow: nixRow(), deployment: nixDeployment(), validateSource: fake.validateSource});
    await settle();
    assert.equal(model.overallStatus(), "trusted");
    assert.equal(model.sourceValid(), true);
    assert.equal(model.nixDockerBuild.selectedCommit.val, SHA_A);
    assert.equal(model.runningInvalidReason(), "");
    assert.equal(fake.requests.length, 0);
    assert.equal(model.toUpdatePayload(), null, "nothing changed");
});

test("update: a trusted source lists lazily through the deployment versions endpoint", async () => {
    const fake = fakeValidate();
    const calls = [];
    const loadDeploymentVersions = async request => {
        calls.push(request);
        return {deploymentId: 7, nixDockerBuild: {branches: ["main", "release"], selectedBranch: "release", commits: [{id: SHA_A, label: "deployed", time: new Date(0)}]}};
    };
    const model = new DeploymentCreationUpdate({mode: "update", deploymentRow: nixRow(), deployment: nixDeployment(), validateSource: fake.validateSource, loadDeploymentVersions});
    await model.ensureVersionsLoaded();
    assert.deepEqual(calls, [{deploymentId: 7}]);
    assert.equal(model.nixDockerBuild.selectedBranch.val, "release");
    assert.equal(model.versionEntry()?.label, "deployed");
    await model.ensureVersionsLoaded();
    assert.equal(calls.length, 1, "loaded once");
    assert.equal(fake.requests.length, 0);
});

test("update: a trusted source selecting another commit skips the flake check", async () => {
    const fake = fakeValidate();
    const model = new DeploymentCreationUpdate({mode: "update", deploymentRow: nixRow(), deployment: nixDeployment(), validateSource: fake.validateSource});
    model.selectVersion(SHA_B);
    await settle();
    assert.equal(fake.requests.length, 0);
    assert.equal(model.overallStatus(), "trusted");
    assert.deepEqual(model.toUpdatePayload().versionOnlyUpdate, {targetVersion: SHA_B});
});

test("update: an edited repository drops trust until validated; the old commit is cleared", async () => {
    const fake = fakeValidate();
    const model = new DeploymentCreationUpdate({mode: "update", deploymentRow: nixRow(), deployment: nixDeployment(), validateSource: fake.validateSource});
    model.form.nixRepo.val = "github.com/acme/other";
    await settle();
    assert.equal(model.overallStatus(), "unvalidated");
    assert.equal(model.nixDockerBuild.selectedCommit.val, "");
    assert.match(model.runningInvalidReason(), /Validate the source/);
    model.form.nixRepo.val = REPO;
    await settle();
    assert.equal(model.overallStatus(), "trusted", "back to the saved tuple");
});

test("update: a stopped deployment may retarget its version as a spec update", async () => {
    const fake = fakeValidate();
    const model = new DeploymentCreationUpdate({mode: "update", deploymentRow: nixRow({running: false}), deployment: nixDeployment({running: false}), validateSource: fake.validateSource});
    model.selectVersion(SHA_B);
    await settle();
    const payload = model.toUpdatePayload();
    assert.equal(payload.specUpdate.spec.container1Spec.version, SHA_B);
    assert.equal(payload.specUpdate.spec.container1Spec.running, false);
});

test("update: a stopped deployment with a partial sha cannot save", async () => {
    const fake = fakeValidate();
    const model = new DeploymentCreationUpdate({mode: "update", deploymentRow: nixRow({running: false}), deployment: nixDeployment({running: false}), validateSource: fake.validateSource});
    model.selectVersion("abc123");
    await settle();
    assert.equal(model.versionInvalidReason(), "Version must be a full 40-character commit sha.");
});

test("code mode: replacing the document re-syncs the source and checks a new commit when valid", async () => {
    const fake = fakeValidate();
    const model = nixCreate(fake);
    await model.validate();
    const document = model.toDocument();
    document.spec.container1Spec.version = SHA_B;
    model.replaceDocument(document);
    await settle();
    assert.equal(model.nixDockerBuild.selectedCommit.val, SHA_B);
    assert.equal(model.overallStatus(), "ok");
    assert.equal(fake.requests.at(-1).nixDockerBuild.selectedCommit.id, SHA_B);

    // A document that changes the repository resets the layers instead.
    const other = model.toDocument();
    other.spec.container1Spec.source.nixDockerBuild.repo = "github.com/acme/other";
    const before = fake.requests.length;
    model.replaceDocument(other);
    await settle();
    assert.equal(model.overallStatus(), "unvalidated");
    assert.equal(fake.requests.length, before, "no request without a validate");
});

test("image: validate lists tags; the reference's own tag pins the version", async () => {
    const fake = fakeValidate();
    const model = new DeploymentCreationUpdate({mode: "create", validateSource: fake.validateSource});
    model.form.sourceType.val = SOURCE_DOCKER_IMAGE;
    model.form.containerImage.val = "docker.io/library/postgres";
    await settle();
    assert.equal(model.overallStatus(), "unvalidated");
    assert.equal(await model.validate(), true);
    assert.equal(fake.requests.length, 1);
    assert.equal(model.containerImage.tags.val.length, 2);
    assert.match(model.layerStatus("image").message, /2 tags/);
    assert.match(model.runningInvalidReason(), /Select a version/);
    model.selectVersion("1.1.0");
    assert.equal(model.runningInvalidReason(), "");
    assert.deepEqual(model.versionOptions().map(item => item.apply), ["1.2.0", "1.1.0"]);

    // A tag typed into the reference selects that version; the repository is
    // unchanged, so the validated layers survive.
    model.form.containerImage.val = "docker.io/library/postgres:18";
    await settle();
    assert.equal(model.explicitImageVersion(), "18");
    assert.equal(model.selectedTargetVersion(), "18");
    assert.equal(model.overallStatus(), "ok");
    assert.equal(model.toDocument().spec.container1Spec.source.remoteImage.image, "docker.io/library/postgres");
    assert.equal(model.toDocument().spec.container1Spec.version, "18");
});

test("image: an unchanged saved image is trusted even when the deployment is stopped", async () => {
    const fake = fakeValidate();
    const deployment = {id: 9, version: 1, def: {name: "db", spaceId: 1, nodeId: 11, spec: {container1Spec: {version: "17", running: false, source: {remoteImage: {image: "docker.io/library/postgres"}}}}}};
    const row = {id: 9, version: 1, spaceId: 1, name: "db", variant: SOURCE_DOCKER_IMAGE, deployedVersion: "17", desiredRunning: false, runnerType: "container"};
    const model = new DeploymentCreationUpdate({mode: "update", deploymentRow: row, deployment, validateSource: fake.validateSource});
    await settle();
    assert.equal(model.form.sourceType.val, SOURCE_DOCKER_IMAGE);
    assert.equal(model.overallStatus(), "trusted");
    model.setDesiredRunning(true);
    assert.equal(model.runningInvalidReason(), "");
    assert.deepEqual(model.toUpdatePayload().versionOnlyUpdate, {targetVersion: "17"});
});
