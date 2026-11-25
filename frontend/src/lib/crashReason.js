const STOPPED = "The agent stopped this container: ";

const CRASH_REASON_TEXT = {
    1: `${STOPPED}its network could not be recovered after an agent restart.`,
    2: `${STOPPED}its address could not be published.`,
    3: `${STOPPED}its readiness listener failed.`,
    4: "The agent gave up on this container: it did not report ready before the readiness timeout.",
};

const CRASH_REASON_LABEL = {
    1: "network recovery failed",
    2: "network publish failed",
    3: "readiness listener failed",
    4: "readiness timed out",
};

export const crashReasonLabel = (reason) => {
    const n = Number(reason || 0);
    if (!n) return '';
    return CRASH_REASON_LABEL[n] || `reason ${n}`;
};

export const crashReasonText = (reason) => {
    const n = Number(reason || 0);
    if (!n) return '';
    return CRASH_REASON_TEXT[n] || "The agent stopped this container. This version of the Web UI does not know why.";
};
