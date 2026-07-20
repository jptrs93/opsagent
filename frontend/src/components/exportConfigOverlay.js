import van from "vanjs-core";
import {capi} from "../capi/index.js";

const {div, h2, p, pre, button} = van.tags;

export function exportConfigOverlay(onClose) {
    const outputText = van.state('');
    const status = van.state('Generating export...');
    const loading = van.state(true);
    const error = van.state('');
    const copied = van.state(false);
    const decoder = new TextDecoder();

    const loadExport = async () => {
        try {
            const res = await capi.postV1GenerateExportedConfig({});
            outputText.val = decoder.decode(res.blob || new Uint8Array(0));
            status.val = 'Export generated. Asset blob contents are omitted.';
        } catch (e) {
            error.val = e.message || 'Failed to generate export';
            status.val = error.val;
        } finally {
            loading.val = false;
        }
    };

    const copyExport = async () => {
        if (!outputText.val) return;
        await navigator.clipboard.writeText(outputText.val);
        copied.val = true;
        setTimeout(() => { copied.val = false; }, 1500);
    };

    const downloadExport = () => {
        if (!outputText.val) return;
        const url = URL.createObjectURL(new Blob([outputText.val], {type: 'application/json'}));
        const link = document.createElement('a');
        link.href = url;
        link.download = 'opendeploy-config-export.json';
        link.click();
        URL.revokeObjectURL(url);
    };

    void loadExport();

    return div(
        div({class: "fixed inset-0 bg-black/70 z-40", onclick: onClose}),
        div(
            {class: "fixed inset-0 z-50 flex items-center justify-center p-3 md:p-6 pointer-events-none", "data-testid": "export-config-overlay"},
            div(
                {class: "w-full h-full max-w-[1200px] max-h-[90vh] bg-gray-900 border border-gray-700 rounded-xl shadow-2xl flex flex-col overflow-hidden pointer-events-auto", onclick: (e) => e.stopPropagation()},
                div(
                    {class: "flex items-center justify-between gap-4 px-4 py-3 border-b border-gray-700"},
                    div({class: "min-w-0"},
                        h2({class: "text-sm font-semibold text-gray-200"}, "Export configuration"),
                        p({class: () => `text-xs ${error.val ? 'text-red-400' : 'text-gray-500'}`}, () => status.val),
                    ),
                    button({class: "text-sm text-gray-400 hover:text-gray-200 cursor-pointer px-3 py-1.5", onclick: onClose}, "Close"),
                ),
                div({class: "flex-1 min-h-0 bg-gray-950 overflow-hidden"},
                    pre(
                        {class: "app-scroll h-full w-full overflow-auto p-4 text-xs font-mono whitespace-pre-wrap break-all leading-5 text-gray-200"},
                        () => outputText.val || (loading.val ? 'Generating export...' : error.val),
                    ),
                ),
                div(
                    {class: "flex items-center justify-between gap-3 px-4 py-3 border-t border-gray-700"},
                    p({class: "text-xs text-gray-500"}, "Secrets are exported as metadata only. Asset blob contents are omitted."),
                    div({class: "flex items-center gap-2"},
                        button({class: "btn-secondary text-sm py-1.5 px-3 cursor-pointer", disabled: () => !outputText.val, onclick: copyExport}, () => copied.val ? 'Copied' : 'Copy'),
                        button({class: "btn-primary text-sm py-1.5 px-3 cursor-pointer", disabled: () => !outputText.val, onclick: downloadExport}, "Download"),
                    ),
                ),
            ),
        ),
    );
}
