import assert from "node:assert/strict";
import {EditorState} from "@codemirror/state";
import {deploymentHcl} from "../../src/hcl/index.js";
import {DeploymentCreationUpdate} from "../../src/components/deploymentCreationUpdate.js";
import {syntaxDiagnostics} from "../../src/components/deploymentConfigCodeWidget.js";
import {deploymentDocumentToHcl, parseDeploymentHcl} from "../../src/components/deploymentHcl.js";
import {
    fixturePresets,
    mockAssets,
    mockConfigRefs,
    mockDeployments,
    mockNodes,
    mockSecretRefs,
    mockSpaces,
} from "./mockData.js";

const catalogs = {
    spaces: mockSpaces,
    nodes: mockNodes,
    assets: mockAssets,
    secretRefs: mockSecretRefs,
    configRefs: mockConfigRefs,
    deployments: [...mockDeployments, {
        config: {id: 998, nodeId: 12, name: 'api', spaceId: 1},
        status: {},
    }],
};

const collisionCatalogs = {
    ...catalogs,
    assets: [...mockAssets, {id: 298, key: 'nginx.conf', format: 'text', spaceId: 2, contentVersions: [{id: 299, version: 4}]}],
    secretRefs: [...mockSecretRefs, {id: 399, name: 'database-password', version: 5, spaceId: 2}],
    configRefs: [...mockConfigRefs, {id: 499, name: 'database-host', version: 3, spaceId: 2}],
    deployments: [...mockDeployments, {
        config: {id: 999, nodeId: 11, name: 'api', spaceId: 2},
        status: {},
    }],
};

function modelFor(preset) {
    return new DeploymentCreationUpdate({
        deployment: preset.deployment,
        deploymentConfig: preset.deploymentConfig,
        validateSource: async () => ({}),
    });
}

for (const presetName of ['updateContainer', 'updateNixStopped']) {
    const model = modelFor(fixturePresets[presetName]);
    const before = model.toDocument();
    const text = deploymentDocumentToHcl(before, catalogs, {pinVersions: true});
    const parsed = parseDeploymentHcl(text, catalogs, {
        immutableName: before.identity.name,
        immutableNodeId: before.nodeId,
        updateMode: true,
        initialVersion: before.spec.container1Spec.version,
    });
    assert.deepEqual(parsed.diagnostics, []);
    assert.ok(parsed.document);
    model.replaceDocument(parsed.document);
    assert.deepEqual(model.toDocument(), before);
}

const canonicalDocument = modelFor(fixturePresets.updateContainer).toDocument();
assert.equal(Object.hasOwn(canonicalDocument, 'desiredState'), false);
assert.deepEqual(canonicalDocument.spec.container1Spec.runtime.assetMounts[0], {
    assetVersionId: 213,
    containerPath: '/etc/api/nginx.conf',
    permission: 2,
});
const canonicalHcl = deploymentDocumentToHcl(canonicalDocument, catalogs, {pinVersions: true});
assert.match(canonicalHcl, /^deployment \{\n  node = node\("London edge"\)\n\n  identity \{/);
assert.match(canonicalHcl, /secret\("database-password", \{ version = 4 \}\)/);
assert.match(canonicalHcl, /config\("database-host", \{ version = 2 \}\)/);
assert.match(canonicalHcl, /asset\("nginx\.conf", \{ version = 3 \}\)/);
assert.match(canonicalHcl, /port_forward\("tcp", 8080, \{ host_port = 8443 \}\)/);
const canonicalEditorState = EditorState.create({doc: canonicalHcl, extensions: [deploymentHcl()]});
assert.deepEqual(syntaxDiagnostics(canonicalEditorState), []);

const latestHcl = deploymentDocumentToHcl(canonicalDocument, catalogs);
assert.match(latestHcl, /secret\("database-password"\)/);
assert.match(latestHcl, /config\("database-host"\)/);
assert.match(latestHcl, /asset\("nginx\.conf"\)/);
assert.doesNotMatch(latestHcl, /(?:secret|config|asset)\("[^"]+", \{ version = \d+ \}\)/);

// A deployment outside the global space referencing global items must carry
// the space explicitly — a bare name would resolve own-space-first and could
// be captured by a later same-named local item.
const crossSpaceDocument = structuredClone(canonicalDocument);
crossSpaceDocument.identity.spaceId = 2;
const crossSpaceHcl = deploymentDocumentToHcl(crossSpaceDocument, catalogs, {pinVersions: true});
assert.doesNotMatch(crossSpaceHcl, /__unresolved/);
assert.match(crossSpaceHcl, /secret\("database-password", \{ space = "production", version = 4 \}\)/);
assert.match(crossSpaceHcl, /config\("database-host", \{ space = "production", version = 2 \}\)/);
assert.match(crossSpaceHcl, /asset\("nginx\.conf", \{ space = "production", version = 3 \}\)/);
const crossSpaceParsed = parseDeploymentHcl(crossSpaceHcl, catalogs);
assert.ok(crossSpaceParsed.document);
assert.equal(crossSpaceParsed.document.spec.container1Spec.runtime.envVars.DATABASE_PASSWORD.secretVersionId, 301);
assert.equal(crossSpaceParsed.document.spec.container1Spec.runtime.envVars.DATABASE_HOST.configVersionId, 401);
assert.equal(crossSpaceParsed.document.spec.container1Spec.runtime.assetMounts[0].assetVersionId, 213);

const missingReferenceDocument = structuredClone(canonicalDocument);
missingReferenceDocument.spec.container1Spec.runtime.envVars.DATABASE_PASSWORD.secretVersionId = 9999;
assert.match(deploymentDocumentToHcl(missingReferenceDocument, catalogs), /secret\("__unresolved_secret_9999"\)/);

const updateModel = modelFor(fixturePresets.updateContainer);
const changedHcl = deploymentDocumentToHcl(updateModel.toDocument(), catalogs, {pinVersions: true})
    .replace('image = "ghcr.io/acme/api"', 'image = "ghcr.io/acme/api-next"')
    .replace('version = "v2.8.1"', 'version = "v3.0.0"');
const changed = parseDeploymentHcl(changedHcl, catalogs, {
    immutableName: 'api',
    immutableNodeId: 11,
    updateMode: true,
    initialVersion: 'v2.8.1',
});
assert.ok(changed.document);
updateModel.replaceDocument(changed.document);
await new Promise(resolve => setTimeout(resolve, 0));
const payload = updateModel.toUpdatePayload();
assert.equal(payload.spec.container1Spec.source.remoteImage.image, 'ghcr.io/acme/api-next');
assert.equal(payload.spec.container1Spec.version, 'v3.0.0');
assert.equal(payload.spec.container1Spec.running, true);
assert.equal(payload.targetVersion, 'v3.0.0');

const createPayload = modelFor(fixturePresets.fork).toCreatePayload();
assert.equal(Object.hasOwn(createPayload, 'desiredState'), false);
assert.equal(createPayload.spec.container1Spec.version, 'v2.8.1');
assert.equal(createPayload.spec.container1Spec.running, true);

const invalidIdentity = parseDeploymentHcl(changedHcl.replace('name = "api"', 'name = "renamed"'), catalogs, {
    immutableName: 'api',
    immutableNodeId: 11,
    updateMode: true,
    initialVersion: 'v2.8.1',
});
assert.equal(invalidIdentity.document, null);
assert.match(invalidIdentity.diagnostics[0].message, /immutable/);

const unusualEnvDocument = structuredClone(updateModel.toDocument());
unusualEnvDocument.spec.container1Spec.runtime.envVars['SERVICE.URL-V2'] = {value: 'https://example.test'};
const unusualEnvParsed = parseDeploymentHcl(deploymentDocumentToHcl(unusualEnvDocument, catalogs), catalogs);
assert.ok(unusualEnvParsed.document);
assert.equal(unusualEnvParsed.document.spec.container1Spec.runtime.envVars['SERVICE.URL-V2'].value, 'https://example.test');

const pinnedModel = modelFor(fixturePresets.updateContainer);
const pinnedHcl = deploymentDocumentToHcl(pinnedModel.toDocument(), collisionCatalogs, {pinVersions: true});
assert.match(pinnedHcl, /secret\("database-password", \{ version = 4 \}\)/);
assert.match(pinnedHcl, /config\("database-host", \{ version = 2 \}\)/);
assert.match(pinnedHcl, /asset\("nginx\.conf", \{ version = 3 \}\)/);
const pinnedParsed = parseDeploymentHcl(pinnedHcl, collisionCatalogs);
assert.equal(pinnedParsed.document.spec.container1Spec.runtime.envVars.DATABASE_PASSWORD.secretVersionId, 301);
assert.equal(pinnedParsed.document.spec.container1Spec.runtime.envVars.DATABASE_HOST.configVersionId, 401);
assert.equal(pinnedParsed.document.spec.container1Spec.runtime.assetMounts[0].assetVersionId, 213);

const latestParsed = parseDeploymentHcl(pinnedHcl
    .replace('secret("database-password", { version = 4 })', 'secret("database-password")')
    .replace('config("database-host", { version = 2 })', 'config("database-host")')
    .replace('asset("nginx.conf", { version = 3 })', 'asset("nginx.conf")'), collisionCatalogs);
assert.ok(latestParsed.document);
// Secret, config, and asset refs are scoped to the deployment's own space plus
// the global space, so the space-2 collisions (399/499/299) are not candidates
// for this space-1 deployment.
assert.equal(latestParsed.document.spec.container1Spec.runtime.envVars.DATABASE_PASSWORD.secretVersionId, 301);
assert.equal(latestParsed.document.spec.container1Spec.runtime.envVars.DATABASE_HOST.configVersionId, 401);
assert.equal(latestParsed.document.spec.container1Spec.runtime.assetMounts[0].assetVersionId, 213);

// Latest-version comparison is per-space: the space-2 database-password v5
// must not force a version pin on the space-1 item whose latest is v4.
const collisionLatestHcl = deploymentDocumentToHcl(canonicalDocument, collisionCatalogs);
assert.match(collisionLatestHcl, /secret\("database-password"\)/);
assert.doesNotMatch(collisionLatestHcl, /\{ space = /);

// Explicit space qualifiers bypass own-space shadowing: the space-2
// deployment's own database-password (399) shadows the bare name, so the
// global pin serializes with its space and still round-trips to 301.
const stagingDocument = structuredClone(canonicalDocument);
stagingDocument.identity.spaceId = 2;
const stagingHcl = deploymentDocumentToHcl(stagingDocument, collisionCatalogs, {pinVersions: true});
assert.match(stagingHcl, /secret\("database-password", \{ space = "production", version = 4 \}\)/);
const stagingParsed = parseDeploymentHcl(stagingHcl, collisionCatalogs);
assert.ok(stagingParsed.document);
assert.equal(stagingParsed.document.spec.container1Spec.runtime.envVars.DATABASE_PASSWORD.secretVersionId, 301);

const shadowParsed = parseDeploymentHcl(stagingHcl.replace(
    'secret("database-password", { space = "production", version = 4 })',
    'secret("database-password")',
), collisionCatalogs);
assert.ok(shadowParsed.document);
assert.equal(shadowParsed.document.spec.container1Spec.runtime.envVars.DATABASE_PASSWORD.secretVersionId, 399);

// Reference locality also applies to explicit qualifiers: only the
// deployment's own space and the global space are allowed.
const localityParsed = parseDeploymentHcl(canonicalHcl.replace(
    'secret("database-password", { version = 4 })',
    'secret("database-password", { space = "staging" })',
), collisionCatalogs);
assert.equal(localityParsed.document, null);
assert.ok(localityParsed.diagnostics.some(item => /may only use the deployment's space or the global space/.test(item.message)));

const unknownSpaceParsed = parseDeploymentHcl(canonicalHcl.replace(
    'secret("database-password", { version = 4 })',
    'secret("database-password", { space = "nope" })',
), catalogs);
assert.equal(unknownSpaceParsed.document, null);
assert.ok(unknownSpaceParsed.diagnostics.some(item => /No space named "nope" exists/.test(item.message)));

const oldNumericVersion = parseDeploymentHcl(latestHcl.replace(
    'config("database-host")',
    'config("database-host", 2)',
), catalogs);
assert.equal(oldNumericVersion.document, null);
assert.ok(oldNumericVersion.diagnostics.some(item => /Options must be an object/.test(item.message)));

const equalPortDocument = structuredClone(pinnedModel.toDocument());
equalPortDocument.spec.networking.portForwarding[0].hostPort = 8080;
const equalPortHcl = deploymentDocumentToHcl(equalPortDocument, catalogs);
assert.match(equalPortHcl, /port_forward\("tcp", 8080\)/);
const equalPortParsed = parseDeploymentHcl(equalPortHcl, catalogs);
assert.deepEqual(equalPortParsed.document.spec.networking.portForwarding[0], {
    protocol: 1,
    hostPort: 8080,
    containerPort: 8080,
});

const oldPortForward = parseDeploymentHcl(canonicalHcl.replace(
    'port_forward("tcp", 8080, { host_port = 8443 })',
    'port_forward("tcp", 8443, 8080)',
), catalogs);
assert.equal(oldPortForward.document, null);
assert.ok(oldPortForward.diagnostics.some(item => /Options must be an object/.test(item.message)));

const oldNestedNode = parseDeploymentHcl(canonicalHcl.replace(
    '  node = node("London edge")\n\n  identity {',
    '  identity {\n    node = node("London edge")',
), catalogs);
assert.equal(oldNestedNode.document, null);
assert.ok(oldNestedNode.diagnostics.some(item => /node is not valid in identity/.test(item.message)));

const qualifiedModel = modelFor(fixturePresets.updateNixStopped);
const qualifiedHcl = deploymentDocumentToHcl(qualifiedModel.toDocument(), collisionCatalogs);
assert.match(qualifiedHcl, /address\("production", "api"\)/);
const qualifiedParsed = parseDeploymentHcl(qualifiedHcl, collisionCatalogs);
assert.equal(qualifiedParsed.document.spec.container1Spec.runtime.envVars.API_ADDRESS.addressDeploymentId, 101);
assert.equal(qualifiedParsed.document.spec.container1Spec.runtime.envVars.API_ADDRESS.addressSpaceId, 1);
const qualifiedEditorState = EditorState.create({doc: qualifiedHcl, extensions: [deploymentHcl()]});
assert.deepEqual(syntaxDiagnostics(qualifiedEditorState), []);

const multilineAddressHcl = qualifiedHcl.replace(
    'address("production", "api")',
    'address(\n        "production",\n        "api",\n      )',
);
assert.ok(parseDeploymentHcl(multilineAddressHcl, collisionCatalogs).document);
assert.deepEqual(syntaxDiagnostics(EditorState.create({doc: multilineAddressHcl, extensions: [deploymentHcl()]})), []);

const malformedAddressHcl = qualifiedHcl.replace(
    'address("production", "api")',
    'address("production", "api"',
);
assert.equal(parseDeploymentHcl(malformedAddressHcl, collisionCatalogs).document, null);
assert.ok(syntaxDiagnostics(EditorState.create({doc: malformedAddressHcl, extensions: [deploymentHcl()]})).length > 0);

const deploymentMountDocument = structuredClone(qualifiedModel.toDocument());
deploymentMountDocument.spec.container1Spec.runtime.crossDeploymentMounts = [{
    deploymentId: 103,
    containerPath: '/mnt/database',
    permission: 2,
}];
const deploymentMountHcl = deploymentDocumentToHcl(deploymentMountDocument, collisionCatalogs);
assert.match(deploymentMountHcl, /deployment\("production", "database"\)/);
const deploymentMountParsed = parseDeploymentHcl(deploymentMountHcl, collisionCatalogs);
assert.equal(deploymentMountParsed.document.spec.container1Spec.runtime.crossDeploymentMounts[0].deploymentId, 103);
assert.deepEqual(syntaxDiagnostics(EditorState.create({doc: deploymentMountHcl, extensions: [deploymentHcl()]})), []);

const oldDeploymentReference = parseDeploymentHcl(qualifiedHcl.replace(
    'address("production", "api")',
    'address("api", space("production"))',
), collisionCatalogs);
assert.equal(oldDeploymentReference.document, null);
assert.ok(oldDeploymentReference.diagnostics.some(item => /address\("space", "deployment"\)/.test(item.message)));

const stoppedHcl = deploymentDocumentToHcl(qualifiedModel.toDocument(), catalogs)
    .replace(qualifiedModel.toDocument().spec.container1Spec.version, 'new-stopped-version');
const stoppedVersionChange = parseDeploymentHcl(stoppedHcl, catalogs, {
    immutableName: 'worker',
    immutableNodeId: 11,
    updateMode: true,
    initialVersion: qualifiedModel.toDocument().spec.container1Spec.version,
});
assert.equal(stoppedVersionChange.document, null);
assert.ok(stoppedVersionChange.diagnostics.some(item => /cannot change while.*stopped/.test(item.message)));

const conflictingImageVersion = structuredClone(updateModel.toDocument());
conflictingImageVersion.spec.container1Spec.source.remoteImage.image = 'ghcr.io/acme/api:v4';
conflictingImageVersion.spec.container1Spec.version = 'v3';
const conflictingImageParsed = parseDeploymentHcl(deploymentDocumentToHcl(conflictingImageVersion, catalogs), catalogs);
assert.equal(conflictingImageParsed.document, null);
assert.ok(conflictingImageParsed.diagnostics.some(item => /must match/.test(item.message)));

console.log('Deployment editor HCL round-trip checks passed.');
