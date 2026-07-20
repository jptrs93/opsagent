import assert from "node:assert/strict";
import {EditorState} from "@codemirror/state";
import {hcl} from "codemirror-lang-hcl";
import {schemaCompletion, syntaxDiagnostics} from "./editor.js";
import {containerExample, nixExample} from "./examples.js";
import {validateDeploymentHcl} from "./schema.js";

for (const example of [containerExample, nixExample]) {
    assert.deepEqual(validateDeploymentHcl(example).filter(item => item.severity === "error"), []);
    const state = EditorState.create({doc: example, extensions: [hcl()]});
    assert.deepEqual(syntaxDiagnostics(state), []);
}

const crossSpaceAddress = containerExample.replace(
    'address("production", "redis.cache")',
    'address("staging", "staging_redis")',
);
assert.deepEqual(validateDeploymentHcl(crossSpaceAddress).filter(item => item.severity === "error"), []);

const obsoleteAddress = containerExample.replace(
    'address("production", "redis.cache")',
    'address("redis.cache")',
);
assert.ok(validateDeploymentHcl(obsoleteAddress).some(item => /address\("space", "deployment"\)/.test(item.message)));

const obsoleteDeployment = containerExample.replace(
    'deployment("production", "report.archive")',
    'deployment("report.archive")',
);
assert.ok(validateDeploymentHcl(obsoleteDeployment).some(item => /deployment\("space", "deployment"\)/.test(item.message)));

const completion = text => schemaCompletion({
    state: EditorState.create({doc: text}),
    pos: text.length,
});
assert.deepEqual(
    completion('address("').options.map(item => item.label),
    ["production", "staging", "development"],
);
assert.deepEqual(
    completion('address("staging", "').options.map(item => item.label),
    ["staging_redis"],
);

console.log("HCL exploration schema checks passed.");
