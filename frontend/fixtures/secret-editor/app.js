import van from "vanjs-core";
import {secretGenerator} from "/src/components/secretGenerator.js";

const {div, h1, h2, p, pre} = van.tags;

const generatorPreview = (widthClass, label) => {
    const generated = van.state("");
    return div(
        {class: widthClass},
        div({class: "mb-2 flex items-baseline justify-between gap-3"},
            h2({class: "text-sm font-medium text-gray-200"}, label),
            p({class: "text-xs text-gray-500"}, "Interactive"),
        ),
        div({class: "overflow-hidden rounded-xl border border-gray-700 bg-gray-900 shadow-xl"},
            secretGenerator({
                onGenerate: value => generated.val = value,
                className: "border-b border-gray-700 bg-gray-950/30 px-4 py-3",
            }),
            div({class: "p-4"},
                p({class: "mb-2 text-xs font-medium text-gray-500"}, "Generated draft"),
                pre({class: "min-h-16 whitespace-pre-wrap break-all font-mono text-sm leading-relaxed text-gray-200"},
                    () => generated.val || "Generate a password or passphrase to preview it here."),
            ),
        ),
    );
};

van.add(document.body,
    div({class: "app-scroll h-full overflow-y-auto p-4 sm:p-8"},
        div({class: "mx-auto flex max-w-5xl flex-col gap-8"},
            div(
                h1({class: "text-lg font-semibold text-white"}, "Secret generator fixture"),
                p({class: "mt-1 text-sm text-gray-400"},
                    "Production generator controls at editor and narrow mobile widths."),
            ),
            generatorPreview("w-full", "Editor width"),
            generatorPreview("w-full max-w-sm", "Narrow width"),
        ),
    ),
);
