import test from "node:test";
import assert from "node:assert/strict";
import {crashReasonLabel, crashReasonText} from "./crashReason.js";

test("a status without a crash reason has no text", () => {
    assert.equal(crashReasonText(0), '');
    assert.equal(crashReasonText(undefined), '');
    assert.equal(crashReasonText(null), '');
});

test("each known crash reason reads as a sentence about the agent", () => {
    const texts = [1, 2, 3, 4].map(crashReasonText);
    for (const text of texts) {
        assert.match(text, /^The agent (stopped|gave up on) this container: .+\.$/);
    }
    assert.equal(new Set(texts).size, 4);
    assert.match(crashReasonText(1), /network could not be recovered after an agent restart/);
    assert.match(crashReasonText(2), /address could not be published/);
    assert.match(crashReasonText(3), /readiness listener failed/);
    assert.match(crashReasonText(4), /readiness timeout/);
});

test("a reason this build does not know still says the agent stopped the container", () => {
    assert.match(crashReasonText(99), /^The agent stopped this container\./);
});

test("the short label names the reason for the history line", () => {
    assert.equal(crashReasonLabel(0), '');
    assert.equal(crashReasonLabel(undefined), '');
    assert.equal(crashReasonLabel(1), "network recovery failed");
    assert.equal(crashReasonLabel(4), "readiness timed out");
    assert.equal(crashReasonLabel(99), "reason 99");
});
