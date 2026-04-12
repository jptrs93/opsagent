import van from "vanjs-core";
import {loginS} from "../state/login.js";
import {onLogout} from "../state/login.js";
import {encodePrepareOutputRequest} from "../capi/model.js";

const { div, h2, span, pre, button } = van.tags;

const parseDeploymentKey = (key) => {
    const parts = key.split(':');
    if (parts.length >= 3) {
        return { environment: parts[0], machine: parts[1], name: parts.slice(2).join(':') };
    }
    return { environment: '', machine: '', name: key };
};

export function prepareOutput(key, seqNo, onClose) {
    const outputText = van.state('');
    const done = van.state(false);
    const endLabel = van.state('Stream ended');
    let abortController = null;
    let startTimer = null;
    let cancelled = false;

    const abortStream = () => {
        cancelled = true;
        if (startTimer) {
            clearTimeout(startTimer);
            startTimer = null;
        }
        if (abortController) {
            abortController.abort();
        }
    };

    const unregisterLogout = onLogout(abortStream);

    const startStream = async (attempt = 0) => {
        if (cancelled) return;
        abortController = new AbortController();
        const token = loginS.val?.token;
        const headers = {
            "Content-Type": "application/x-protobuf",
            ...(token ? { Authorization: `Bearer ${token}` } : {}),
        };

        try {
            const response = await fetch('/v1/prepare/output', {
                method: 'POST',
                headers,
                body: encodePrepareOutputRequest({ id: parseDeploymentKey(key), seqNo: seqNo }),
                credentials: 'include',
                signal: abortController.signal,
            });

            if (!response.ok) {
                if (response.status === 404 && attempt < 1) {
                    await new Promise(r => setTimeout(r, 1000));
                    return startStream(attempt + 1);
                }
                if (response.status === 404) {
                    endLabel.val = 'No log file found';
                    outputText.val = 'No log file found.';
                } else {
                    outputText.val = `Error: HTTP ${response.status}`;
                }
                done.val = true;
                return;
            }

            const reader = response.body.getReader();
            const decoder = new TextDecoder();

            while (true) {
                const { value, done: streamDone } = await reader.read();
                if (streamDone) break;
                outputText.val += decoder.decode(value, { stream: true });
            }
        } catch (e) {
            if (e.name !== 'AbortError') {
                endLabel.val = 'Connection error';
            }
        } finally {
            unregisterLogout();
        }
        done.val = true;
    };

    const cleanup = () => {
        unregisterLogout();
        abortStream();
        onClose();
    };

    startTimer = setTimeout(() => {
        startTimer = null;
        void startStream();
    }, 0);

    const outputPre = pre(
        {class: "flex-1 overflow-auto p-4 text-xs font-mono whitespace-pre-wrap break-all leading-5"},
        () => outputText.val || 'Waiting for output...',
    );

    // Auto-scroll to bottom when new content arrives
    van.derive(() => {
        outputText.val;
        setTimeout(() => { outputPre.scrollTop = outputPre.scrollHeight; }, 0);
    });

    return div(
        {class: "w-1/2 min-h-0 border-l border-gray-700 bg-gray-900 flex flex-col h-full"},
        div(
            {class: "flex items-center justify-between p-3 border-b border-gray-700"},
            h2({class: "text-sm font-semibold text-gray-300"}, `Prepare: ${key}`),
            div(
                {class: "flex items-center gap-2"},
                () => done.val
                    ? span({class: `text-xs ${endLabel.val === 'Connection error' ? 'text-red-400' : 'text-gray-500'}`}, endLabel.val)
                    : span({class: "text-xs text-blue-400 animate-pulse"}, "Streaming..."),
                button({
                    class: "text-gray-400 hover:text-gray-200 text-sm px-2",
                    onclick: cleanup,
                }, "Close"),
            ),
        ),
        div({class: "flex-1 min-h-0"}, outputPre),
    );
}
