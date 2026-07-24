import assert from "node:assert/strict";
import van from "vanjs-core";
import {createEditorValueBridge} from "../../src/components/assetCodeEditor.js";
import {createValueEditorState} from "../../src/components/valueOverlay.js";
import {decodeSecretSetRequest, encodeSecretSetRequest} from "../../src/capi/model.js";

const editor = createValueEditorState("persisted\r\nvalue");
assert.equal(editor.originalValue.val, "persisted\r\nvalue");
assert.equal(editor.stagedValue.val, "persisted\r\nvalue");
assert.equal(editor.isDirty(), false);

editor.stagedValue.val = "generated-once";
assert.equal(editor.originalValue.val, "persisted\r\nvalue");
assert.equal(editor.isDirty(), true);
editor.stagedValue.val = "generated-twice";
assert.equal(editor.stagedValue.val, "generated-twice");
editor.discard();
assert.equal(editor.stagedValue.val, "persisted\r\nvalue");
assert.equal(editor.isDirty(), false);

// External synchronization must not write back while VanJS captures dependencies.
// Otherwise VanJS drops the dependency and only the first external update is rendered.
const stagedValue = van.state("original");
const bridge = createEditorValueBridge(stagedValue);
let renderedValue = "";
let renderCount = 0;
van.derive(() => {
    const next = stagedValue.val;
    if (next !== renderedValue) {
        bridge.applyExternalValue(() => {
            renderedValue = next;
            bridge.updateFromEditor(renderedValue);
        });
    }
    renderCount += 1;
});

stagedValue.val = "generated-once";
await new Promise(resolve => setTimeout(resolve, 0));
assert.equal(renderedValue, "generated-once");
stagedValue.val = "generated-twice";
await new Promise(resolve => setTimeout(resolve, 0));
assert.equal(renderedValue, "generated-twice");
assert.equal(renderCount, 3);

bridge.updateFromEditor("typed-value");
assert.equal(stagedValue.val, "typed-value");

const setRequest = decodeSecretSetRequest(encodeSecretSetRequest({
    name: "database-password",
    value: new TextEncoder().encode("new-value"),
    updateReferencingDeployments: true,
    referencingDeployments: [{id: 10, version: 3}, {id: 20, version: 7}],
}));
assert.equal(setRequest.updateReferencingDeployments, true);
assert.deepEqual(setRequest.referencingDeployments, [{id: 10, version: 3}, {id: 20, version: 7}]);

console.log("Secret editor state checks passed.");
