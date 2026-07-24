import van from "vanjs-core";
import {xIcon} from "../lib/icons.js";

const {button, div, h2, p, textarea} = van.tags;

export function assetPreviewOverlay(assetMeta, loadAsset, onClose) {
    const asset = van.state(null);
    const error = van.state('');
    const loading = van.state(true);

    void (async () => {
        try {
            if (typeof loadAsset !== 'function') throw new Error('Asset preview is unavailable');
            asset.val = await loadAsset({key: assetMeta.key, version: assetMeta.version || 0});
        } catch (e) {
            error.val = e.message || 'Failed to load asset';
        } finally {
            loading.val = false;
        }
    })();

    const content = () => new TextDecoder().decode(asset.val?.blob || new Uint8Array());

    return div(
        div({class: "fixed inset-0 z-[60] bg-black/75", onclick: onClose}),
        div(
            {class: "fixed inset-0 z-[70] flex items-center justify-center p-3 md:p-6 pointer-events-none", "data-testid": "asset-preview-overlay"},
            div(
                {
                    class: "w-full h-full max-w-5xl max-h-[85vh] bg-gray-900 border border-gray-700 rounded-xl shadow-2xl flex flex-col overflow-hidden pointer-events-auto",
                    role: "dialog",
                    "aria-modal": "true",
                    "aria-labelledby": "asset-preview-title",
                    onclick: e => e.stopPropagation(),
                },
                div(
                    {class: "flex items-center justify-between gap-4 border-b border-gray-700 px-4 py-3"},
                    h2(
                        {id: "asset-preview-title", class: "min-w-0 truncate font-mono text-sm font-semibold text-gray-100"},
                        `${assetMeta.key} v${assetMeta.version || '?'}`,
                    ),
                    button({
                        type: "button",
                        class: "inline-flex items-center gap-1.5 px-2.5 py-1.5 text-sm text-gray-400 hover:text-gray-100 cursor-pointer",
                        onclick: onClose,
                    }, xIcon({size: 16}), "Close"),
                ),
                () => loading.val
                    ? div({class: "flex flex-1 items-center justify-center text-sm text-gray-400"}, "Loading asset content...")
                    : error.val
                        ? div({class: "flex flex-1 items-center justify-center p-6"}, p({class: "text-sm text-red-400"}, error.val))
                        : asset.val?.location
                            ? div({class: "flex flex-1 items-center justify-center p-6"}, p({class: "text-sm text-gray-400"}, "This asset is too large to preview."))
                            : textarea({
                                class: "w-full flex-1 min-h-0 resize-none bg-gray-950 p-4 font-mono text-sm leading-6 text-gray-200 outline-none",
                                readOnly: true,
                                spellcheck: "false",
                                value: content,
                            }),
            ),
        ),
    );
}
