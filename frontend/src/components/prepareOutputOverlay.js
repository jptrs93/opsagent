import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {onLogout} from "../state/login.js";

const {div, h2, span, pre, button} = van.tags;

export function prepareOutputOverlay(deploymentId, deploymentLabel, onClose) {
    const outputText = van.state('');
    const status = van.state('Streaming...');
    const done = van.state(false);
    const abortController = new AbortController();
    const decoder = new TextDecoder();

    const unregisterLogout = onLogout(() => abortController.abort());
    abortController.signal.addEventListener('abort', () => unregisterLogout(), {once: true});

    const close = () => {
        abortController.abort();
        onClose();
    };

    const startStream = async () => {
        try {
            for await (const chunk of capi.postV1DeploymentPrepareOutput({deploymentId, version: 0}, {signal: abortController.signal})) {
                if (chunk.data?.length) {
                    outputText.val += decoder.decode(chunk.data, {stream: true});
                }
            }
            const rest = decoder.decode();
            if (rest) outputText.val += rest;
            status.val = 'Stream ended';
        } catch (e) {
            if (e.name !== 'AbortError') {
                status.val = e.message || 'Connection error';
                if (!outputText.val) outputText.val = status.val;
            }
        } finally {
            done.val = true;
        }
    };

    void startStream();

    const outputPre = pre(
        {"data-testid": "prepare-output-text", class: "h-full w-full overflow-auto bg-gray-950 p-4 text-xs font-mono whitespace-pre-wrap break-all leading-5 text-gray-200"},
        () => outputText.val || 'Waiting for prepare output...',
    );

    van.derive(() => {
        outputText.val;
        setTimeout(() => { outputPre.scrollTop = outputPre.scrollHeight; }, 0);
    });

    return div(
        div({class: "fixed inset-0 bg-black/70 z-40", onclick: close}),
        div(
            {class: "fixed inset-0 z-50 flex items-center justify-center p-3 md:p-6 pointer-events-none", "data-testid": "prepare-output-overlay"},
            div(
                {class: "w-full h-full max-w-[1600px] max-h-[94vh] bg-gray-900 border border-gray-700 rounded-xl shadow-2xl flex flex-col overflow-hidden pointer-events-auto", onclick: (e) => e.stopPropagation()},
                div(
                    {class: "flex items-center justify-between gap-4 px-4 py-3 border-b border-gray-700"},
                    div(
                        {class: "min-w-0"},
                        h2({class: "text-sm font-semibold text-gray-200 truncate"}, `Prepare output: ${deploymentLabel}`),
                        span({class: () => `text-xs ${done.val && status.val !== 'Stream ended' ? 'text-red-400' : 'text-gray-500'}`}, () => status.val),
                    ),
                    button({class: "text-sm text-gray-400 hover:text-gray-200 cursor-pointer px-3 py-1.5", onclick: close}, "Close"),
                ),
                div({class: "flex-1 min-h-0 bg-gray-950 overflow-hidden"}, outputPre),
            ),
        ),
    );
}
