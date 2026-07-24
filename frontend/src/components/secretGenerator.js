import {wordlist} from "@scure/bip39/wordlists/english";
import van from "vanjs-core";

const {button, div, input, label, option, p, select, span} = van.tags;
const PASSWORD_ALPHANUMERIC = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789";
const PASSWORD_SYMBOLS = "!@#$%^&*()-_=+[]{}";

const stateValue = value => typeof value === "function" ? value() : (value?.val ?? value);

const randomIndex = (length) => {
    const values = new Uint32Array(1);
    const limit = 0x100000000 - (0x100000000 % length);
    do globalThis.crypto.getRandomValues(values); while (values[0] >= limit);
    return values[0] % length;
};

export const generatePassword = (length, includeSymbols = true) => {
    const chars = includeSymbols ? PASSWORD_ALPHANUMERIC + PASSWORD_SYMBOLS : PASSWORD_ALPHANUMERIC;
    let result = "";
    for (let i = 0; i < length; i++) result += chars[randomIndex(chars.length)];
    return result;
};

export const generatePassphrase = (wordCount, separator = "-") => Array.from(
    {length: wordCount},
    () => wordlist[randomIndex(wordlist.length)],
).join(separator);

export function secretGenerator({onGenerate, disabled = false, className = ""}) {
    const type = van.state("password");
    const passwordLength = van.state("32");
    const includeSymbols = van.state(true);
    const passphraseWords = van.state("6");
    const passphraseSeparator = van.state("-");
    const isDisabled = () => Boolean(stateValue(disabled));

    const generate = () => {
        if (isDisabled()) return;
        if (type.val === "passphrase") {
            const count = Math.max(2, Math.min(64, Number.parseInt(passphraseWords.val, 10) || 6));
            passphraseWords.val = String(count);
            onGenerate(generatePassphrase(count, passphraseSeparator.val));
            return;
        }
        const length = Math.max(1, Math.min(4096, Number.parseInt(passwordLength.val, 10) || 32));
        passwordLength.val = String(length);
        onGenerate(generatePassword(length, includeSymbols.val));
    };

    const compactInputClass = "input h-8 w-16 font-mono text-xs tabular-nums";
    return div(
        {class: `flex flex-col gap-2.5 ${className}`},
        p({class: "text-xs font-medium text-gray-400"}, "Generate new secret"),
        div({class: "flex flex-wrap items-center gap-x-3 gap-y-2"},
            select({
                class: "input h-8 w-auto shrink-0 text-xs font-medium",
                value: type,
                disabled,
                onchange: event => type.val = event.target.value,
                "aria-label": "Secret generator type",
            },
            option({value: "password"}, "Password"),
            option({value: "passphrase"}, "Passphrase")),
            () => type.val === "password" ? span({class: "contents"},
                label({class: "inline-flex h-8 items-center gap-2 whitespace-nowrap text-xs text-gray-400"},
                    "Length",
                    input({
                        class: compactInputClass,
                        type: "number",
                        min: "1",
                        max: "4096",
                        value: passwordLength,
                        disabled,
                        oninput: event => passwordLength.val = event.target.value,
                        "aria-label": "Generated password length",
                    })),
                label({class: "inline-flex h-8 items-center gap-2 whitespace-nowrap rounded-md border border-gray-700 bg-gray-900/60 px-2.5 text-xs text-gray-300"},
                    input({
                        class: "accent-blue-500",
                        type: "checkbox",
                        checked: includeSymbols,
                        disabled,
                        onchange: event => includeSymbols.val = event.target.checked,
                    }),
                    "Symbols"),
            ) : span({class: "contents"},
                label({class: "inline-flex h-8 items-center gap-2 whitespace-nowrap text-xs text-gray-400"},
                    "Words",
                    input({
                        class: compactInputClass,
                        type: "number",
                        min: "2",
                        max: "64",
                        value: passphraseWords,
                        disabled,
                        oninput: event => passphraseWords.val = event.target.value,
                        "aria-label": "Generated passphrase word count",
                    })),
                label({class: "inline-flex h-8 items-center gap-2 whitespace-nowrap text-xs text-gray-400"},
                    "Separator",
                    select({
                        class: "input h-8 w-auto text-xs",
                        value: passphraseSeparator,
                        disabled,
                        onchange: event => passphraseSeparator.val = event.target.value,
                    },
                    option({value: "-"}, "Hyphen"),
                    option({value: " "}, "Space"),
                    option({value: "_"}, "Underscore"),
                    option({value: "."}, "Period"))),
            ),
            button({
                type: "button",
                disabled,
                class: () => `ml-auto h-8 rounded-md px-3 text-xs font-medium transition-colors ${isDisabled()
                    ? "cursor-not-allowed bg-gray-700 text-gray-400 opacity-50"
                    : "cursor-pointer bg-brand text-white hover:bg-blue-600"}`,
                onclick: generate,
            }, "Generate"),
        ),
    );
}
