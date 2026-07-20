import assert from "node:assert/strict";
import {EditorState} from "@codemirror/state";
import {hcl} from "codemirror-lang-hcl";
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
    deployments: mockDeployments,
};

const collisionCatalogs = {
    ...catalogs,
    assets: [...mockAssets, {id: 299, key: 'nginx.conf', version: 4, format: 'text', spaceId: 2}],
    secretRefs: [...mockSecretRefs, {id: 399, name: 'database-password', version: 5, spaceId: 2}],
    configRefs: [...mockConfigRefs, {id: 499, name: 'database-host', version: 3, spaceId: 2}],
    deployments: [...mockDeployments, {
        config: {id: 999, nodeId: 11, identity: {name: 'api', spaceId: 2}},
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
        initialVersion: before.desiredState.version,
    });
    assert.deepEqual(parsed.diagnostics, []);
    assert.ok(parsed.document);
    model.replaceDocument(parsed.document);
    assert.deepEqual(model.toDocument(), before);
}

const canonicalDocument = modelFor(fixturePresets.updateContainer).toDocument();
const canonicalHcl = deploymentDocumentToHcl(canonicalDocument, catalogs, {pinVersions: true});
assert.match(canonicalHcl, /^deployment \{\n  node = node\("London edge"\)\n\n  identity \{/);
assert.match(canonicalHcl, /secret\("database-password", \{ version = 4 \}\)/);
assert.match(canonicalHcl, /config\("database-host", \{ version = 2 \}\)/);
assert.match(canonicalHcl, /asset\("nginx\.conf", \{ version = 3 \}\)/);
assert.match(canonicalHcl, /port_forward\("tcp", 8080, \{ host_port = 8443 \}\)/);
const canonicalEditorState = EditorState.create({doc: canonicalHcl, extensions: [hcl()]});
assert.deepEqual(syntaxDiagnostics(canonicalEditorState), []);

const latestHcl = deploymentDocumentToHcl(canonicalDocument, catalogs);
assert.match(latestHcl, /secret\("database-password"\)/);
assert.match(latestHcl, /config\("database-host"\)/);
assert.match(latestHcl, /asset\("nginx\.conf"\)/);
assert.doesNotMatch(latestHcl, /(?:secret|config|asset)\("[^"]+", \{ version = \d+ \}\)/);

const crossSpaceDocument = structuredClone(canonicalDocument);
crossSpaceDocument.identity.spaceId = 2;
const crossSpaceHcl = deploymentDocumentToHcl(crossSpaceDocument, catalogs, {pinVersions: true});
assert.doesNotMatch(crossSpaceHcl, /__unresolved/);
assert.match(crossSpaceHcl, /secret\("database-password", \{ version = 4 \}\)/);
assert.match(crossSpaceHcl, /config\("database-host", \{ version = 2 \}\)/);
assert.match(crossSpaceHcl, /asset\("nginx\.conf", \{ version = 3 \}\)/);
const crossSpaceParsed = parseDeploymentHcl(crossSpaceHcl, catalogs);
assert.ok(crossSpaceParsed.document);
assert.equal(crossSpaceParsed.document.spec.runner.container.envVars.DATABASE_PASSWORD.secretId, 301);
assert.equal(crossSpaceParsed.document.spec.runner.container.envVars.DATABASE_HOST.configId, 401);
assert.equal(crossSpaceParsed.document.spec.runner.container.assetMounts[0].assetId, 201);

const missingReferenceDocument = structuredClone(canonicalDocument);
missingReferenceDocument.spec.runner.container.envVars.DATABASE_PASSWORD.secretId = 9999;
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
assert.equal(payload.spec.prepare.containerImage.image, 'ghcr.io/acme/api-next');
assert.equal(payload.targetVersion, 'v3.0.0');

const invalidIdentity = parseDeploymentHcl(changedHcl.replace('name = "api"', 'name = "renamed"'), catalogs, {
    immutableName: 'api',
    immutableNodeId: 11,
    updateMode: true,
    initialVersion: 'v2.8.1',
});
assert.equal(invalidIdentity.document, null);
assert.match(invalidIdentity.diagnostics[0].message, /immutable/);

const unusualEnvDocument = structuredClone(updateModel.toDocument());
unusualEnvDocument.spec.runner.container.envVars['SERVICE.URL-V2'] = {value: 'https://example.test'};
const unusualEnvParsed = parseDeploymentHcl(deploymentDocumentToHcl(unusualEnvDocument, catalogs), catalogs);
assert.ok(unusualEnvParsed.document);
assert.equal(unusualEnvParsed.document.spec.runner.container.envVars['SERVICE.URL-V2'].value, 'https://example.test');

const pinnedModel = modelFor(fixturePresets.updateContainer);
const pinnedHcl = deploymentDocumentToHcl(pinnedModel.toDocument(), collisionCatalogs, {pinVersions: true});
assert.match(pinnedHcl, /secret\("database-password", \{ version = 4 \}\)/);
assert.match(pinnedHcl, /config\("database-host", \{ version = 2 \}\)/);
assert.match(pinnedHcl, /asset\("nginx\.conf", \{ version = 3 \}\)/);
const pinnedParsed = parseDeploymentHcl(pinnedHcl, collisionCatalogs);
assert.equal(pinnedParsed.document.spec.runner.container.envVars.DATABASE_PASSWORD.secretId, 301);
assert.equal(pinnedParsed.document.spec.runner.container.envVars.DATABASE_HOST.configId, 401);
assert.equal(pinnedParsed.document.spec.runner.container.assetMounts[0].assetId, 201);

const latestParsed = parseDeploymentHcl(pinnedHcl
    .replace('secret("database-password", { version = 4 })', 'secret("database-password")')
    .replace('config("database-host", { version = 2 })', 'config("database-host")')
    .replace('asset("nginx.conf", { version = 3 })', 'asset("nginx.conf")'), collisionCatalogs);
assert.ok(latestParsed.document);
assert.equal(latestParsed.document.spec.runner.container.envVars.DATABASE_PASSWORD.secretId, 399);
assert.equal(latestParsed.document.spec.runner.container.envVars.DATABASE_HOST.configId, 499);
assert.equal(latestParsed.document.spec.runner.container.assetMounts[0].assetId, 299);

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
assert.equal(qualifiedParsed.document.spec.runner.container.envVars.API_ADDRESS.addressDeploymentId, 101);
assert.equal(qualifiedParsed.document.spec.runner.container.envVars.API_ADDRESS.addressSpaceId, 1);
const qualifiedEditorState = EditorState.create({doc: qualifiedHcl, extensions: [hcl()]});
assert.deepEqual(syntaxDiagnostics(qualifiedEditorState), []);

const multilineAddressHcl = qualifiedHcl.replace(
    'address("production", "api")',
    'address(\n        "production",\n        "api",\n      )',
);
assert.ok(parseDeploymentHcl(multilineAddressHcl, collisionCatalogs).document);
assert.deepEqual(syntaxDiagnostics(EditorState.create({doc: multilineAddressHcl, extensions: [hcl()]})), []);

const malformedAddressHcl = qualifiedHcl.replace(
    'address("production", "api")',
    'address("production", "api"',
);
assert.equal(parseDeploymentHcl(malformedAddressHcl, collisionCatalogs).document, null);
assert.ok(syntaxDiagnostics(EditorState.create({doc: malformedAddressHcl, extensions: [hcl()]})).length > 0);

const deploymentMountDocument = structuredClone(qualifiedModel.toDocument());
deploymentMountDocument.spec.runner.container.mounts = [{
    host: '/var/lib/opendeploy-volumes/103/default',
    container: '/mnt/database',
    readonly: true,
}];
const deploymentMountHcl = deploymentDocumentToHcl(deploymentMountDocument, collisionCatalogs);
assert.match(deploymentMountHcl, /deployment\("production", "database"\)/);
const deploymentMountParsed = parseDeploymentHcl(deploymentMountHcl, collisionCatalogs);
assert.equal(deploymentMountParsed.document.spec.runner.container.mounts[0].host, '/var/lib/opendeploy-volumes/103/default');
assert.deepEqual(syntaxDiagnostics(EditorState.create({doc: deploymentMountHcl, extensions: [hcl()]})), []);

const oldDeploymentReference = parseDeploymentHcl(qualifiedHcl.replace(
    'address("production", "api")',
    'address("api", space("production"))',
), collisionCatalogs);
assert.equal(oldDeploymentReference.document, null);
assert.ok(oldDeploymentReference.diagnostics.some(item => /address\("space", "deployment"\)/.test(item.message)));

const stoppedHcl = deploymentDocumentToHcl(qualifiedModel.toDocument(), catalogs)
    .replace(qualifiedModel.toDocument().desiredState.version, 'new-stopped-version');
const stoppedVersionChange = parseDeploymentHcl(stoppedHcl, catalogs, {
    immutableName: 'worker',
    immutableNodeId: 11,
    updateMode: true,
    initialVersion: qualifiedModel.toDocument().desiredState.version,
});
assert.equal(stoppedVersionChange.document, null);
assert.ok(stoppedVersionChange.diagnostics.some(item => /cannot change while.*stopped/.test(item.message)));

const conflictingImageVersion = structuredClone(updateModel.toDocument());
conflictingImageVersion.spec.prepare.containerImage.image = 'ghcr.io/acme/api:v4';
conflictingImageVersion.desiredState.version = 'v3';
const conflictingImageParsed = parseDeploymentHcl(deploymentDocumentToHcl(conflictingImageVersion, catalogs), catalogs);
assert.equal(conflictingImageParsed.document, null);
assert.ok(conflictingImageParsed.diagnostics.some(item => /must match/.test(item.message)));

console.log('Deployment editor HCL round-trip checks passed.');
