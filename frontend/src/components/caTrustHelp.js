// caTrustHelp is the in-page copy of the instructions the installer prints:
// how to make the browser trust the locally generated Web UI CA. It opens as
// an overlay from the login page whenever the server is serving under that
// CA, so someone who continued through the browser warning can install the
// CA without leaving the page.
//
// The content is three steps. Get the certificate: download, copy the PEM,
// and a summary with the CA's SHA-256 fingerprint to check against the
// installer output. Trust it: a platform picker over one code block per
// command. Restart. The PEM is fetched once and inlined into each command as
// a heredoc, so trusting the CA is a single paste with no file to move
// around; until the PEM has arrived, or if it cannot be fetched, the
// file-based commands show.
import van from "vanjs-core";
import {alertCircleIcon, checkIcon, closeIcon, copyIcon, downloadIcon, shieldCheckIcon} from "../lib/icons.js";

const {a, button, div, p, pre, span} = van.tags;

const heredoc = (pem) => `<<'EOF'\n${pem}\nEOF`;

// A step is [title, buildCommand(pem)] where pem is '' when unavailable.
const PLATFORMS = [
    {key: "macos", label: "macOS", steps: [
        ["Write the CA and trust it", (pem) => pem
            ? `cat > opendeploy-ca.crt ${heredoc(pem)}\nsudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain opendeploy-ca.crt`
            : "sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain opendeploy-ca.crt"],
    ]},
    {key: "linux", label: "Linux", steps: [
        ["System store", (pem) => pem
            ? `sudo tee /usr/local/share/ca-certificates/opendeploy-ca.crt >/dev/null ${heredoc(pem)}\nsudo update-ca-certificates`
            : "sudo cp opendeploy-ca.crt /usr/local/share/ca-certificates/ && sudo update-ca-certificates"],
        ["Chrome / Chromium also", (pem) => pem
            ? `certutil -d sql:$HOME/.pki/nssdb -A -t "C,," -n "OpenDeploy Local CA" ${heredoc(pem)}`
            : 'certutil -d sql:$HOME/.pki/nssdb -A -t "C,," -n "OpenDeploy Local CA" -i opendeploy-ca.crt'],
    ]},
    {key: "windows", label: "Windows (PowerShell)", steps: [
        ["Write the CA and trust it", (pem) => pem
            ? `@'\n${pem}\n'@ | Set-Content -Path opendeploy-ca.crt\ncertutil -addstore -f ROOT opendeploy-ca.crt`
            : "certutil -addstore -f ROOT opendeploy-ca.crt"],
    ]},
    {key: "firefox", label: "Firefox", steps: [
        ["Use the OS store: set this to true in about:config", () => "security.enterprise_roots.enabled"],
        ["Or import the downloaded file under", () => "Settings › Privacy & Security › Certificates › View Certificates › Import"],
    ]},
];

// sha256Fingerprint hashes the DER inside a PEM block, colon-separated, the
// form the installer prints. Empty when WebCrypto is unavailable or the PEM
// does not decode.
async function sha256Fingerprint(pem) {
    try {
        if (!crypto?.subtle) return "";
        const b64 = pem.replace(/-----[^-]+-----/g, "").replace(/\s+/g, "");
        const bytes = Uint8Array.from(atob(b64), (c) => c.charCodeAt(0));
        const hash = new Uint8Array(await crypto.subtle.digest("SHA-256", bytes));
        return [...hash].map((b) => b.toString(16).padStart(2, "0")).join(":");
    } catch {
        return "";
    }
}

function caTrustHelpBody() {
    const caURL = `${window.location.origin}/v1/tls/ca.crt`;
    const tab = van.state("macos");
    const copied = van.state(false);
    const copyErr = van.state("");
    const pemS = van.state("");
    const fingerprint = van.state("");

    const fetchPEM = async () => {
        const res = await fetch(caURL);
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        const text = (await res.text()).trim();
        if (!text.startsWith("-----BEGIN CERTIFICATE-----")) throw new Error("not a PEM certificate");
        pemS.val = text;
        fingerprint.val = await sha256Fingerprint(text);
        return text;
    };
    fetchPEM().catch(() => {});

    const copyText = async (text, done) => {
        copyErr.val = "";
        try {
            await navigator.clipboard.writeText(text);
            done.val = true;
            setTimeout(() => { done.val = false; }, 1500);
        } catch (e) {
            copyErr.val = `Copy failed: ${e.message}`;
        }
    };

    const step = (n, title, ...body) => div(
        {class: "flex gap-3"},
        span({class: "mt-px flex h-5 w-5 shrink-0 items-center justify-center rounded-full bg-gray-800 text-[11px] font-medium text-gray-300 ring-1 ring-gray-700"}, String(n)),
        div({class: "flex min-w-0 flex-1 flex-col gap-2.5"}, p({class: "text-sm font-medium text-gray-200"}, title), ...body),
    );

    const chip = "inline-flex items-center gap-1.5 rounded-md border border-gray-600 bg-gray-800/60 px-2.5 py-1 text-xs text-gray-200 transition-colors hover:bg-gray-700 cursor-pointer no-underline";

    const fact = (term, value, mono) => div(
        {class: "flex items-baseline gap-3 px-3 py-1.5"},
        span({class: "w-16 shrink-0 text-[11px] uppercase tracking-wide text-gray-500"}, term),
        span({class: `min-w-0 break-all text-xs text-gray-300 ${mono ? "font-mono" : ""}`}, value),
    );

    const platformPicker = div(
        {class: "inline-flex flex-wrap gap-0.5 rounded-md border border-gray-700 bg-gray-900/60 p-0.5", role: "tablist"},
        ...PLATFORMS.map(({key, label}) => button({
            type: "button",
            role: "tab",
            "aria-selected": () => String(tab.val === key),
            class: () => `cursor-pointer rounded px-2.5 py-1 text-xs transition-colors ${tab.val === key
                ? "bg-gray-700 text-white shadow-sm"
                : "text-gray-400 hover:text-gray-200"}`,
            onclick: () => { tab.val = key; },
        }, label)),
    );

    // One code block per command: a header bar with the step title and its
    // copy button over the command. The block shows exactly what the
    // clipboard gets, PEM included; the "Copied" state is per block.
    const codeBlock = (title, build) => {
        const done = van.state(false);
        const full = () => build(pemS.val);
        return div(
            {class: "overflow-hidden rounded-md border border-gray-700/80"},
            div(
                {class: "flex items-center justify-between gap-2 bg-gray-900/70 px-3 py-1.5"},
                span({class: "truncate text-[11px] text-gray-400"}, title),
                button({
                    type: "button",
                    class: "flex shrink-0 cursor-pointer items-center gap-1 text-[11px] text-gray-400 transition-colors hover:text-gray-100",
                    title: "Copy command",
                    "data-testid": "login-ca-cmd-copy",
                    onclick: () => copyText(full(), done),
                }, () => done.val ? checkIcon({class: "h-3 w-3 text-emerald-400"}) : copyIcon({class: "h-3 w-3"}), () => done.val ? "Copied" : "Copy"),
            ),
            pre({class: "app-scroll max-h-44 overflow-auto whitespace-pre-wrap break-all bg-code px-3 py-2 font-mono text-[11px] leading-relaxed text-gray-300", "data-testid": "login-ca-cmd"}, full),
        );
    };

    return div(
        {class: "flex min-w-0 flex-col gap-5"},
        step(1, "Get the certificate",
            div(
                {class: "flex flex-wrap items-center gap-2"},
                a({class: chip, href: caURL, download: "opendeploy-ca.crt", "data-testid": "login-ca-download"}, downloadIcon({class: "h-3.5 w-3.5"}), "Download opendeploy-ca.crt"),
                button({type: "button", class: chip, "data-testid": "login-ca-copy", onclick: async () => {
                    copyErr.val = "";
                    try {
                        await navigator.clipboard.writeText(pemS.val || await fetchPEM());
                        copied.val = true;
                        setTimeout(() => { copied.val = false; }, 1500);
                    } catch (e) {
                        copyErr.val = `Copy failed: ${e.message}`;
                    }
                }}, () => copied.val ? checkIcon({class: "h-3.5 w-3.5 text-emerald-400"}) : copyIcon({class: "h-3.5 w-3.5"}), () => copied.val ? "Copied" : "Copy PEM"),
                () => copyErr.val ? span({class: "text-xs text-red-400"}, copyErr.val) : "",
            ),
            div(
                {class: "divide-y divide-gray-800 rounded-md border border-gray-700/80 bg-gray-900/50"},
                fact("Issuer", "OpenDeploy Local CA"),
                fact("Serves", window.location.host, true),
                () => fingerprint.val ? fact("SHA-256", fingerprint.val, true) : "",
            ),
        ),
        step(2, "Trust it on this machine",
            platformPicker,
            () => {
                const active = PLATFORMS.find((x) => x.key === tab.val);
                return div({class: "flex flex-col gap-2"}, ...active.steps.map(([title, build]) => codeBlock(title, build)));
            },
        ),
        step(3, "Restart the browser, then reload this page"),
        p({class: "flex items-start gap-2 rounded-md border border-amber-900/50 bg-amber-950/30 px-3 py-2 text-xs leading-relaxed text-amber-200/90", "data-testid": "login-ca-warning"},
            alertCircleIcon({class: "mt-0.5 h-3.5 w-3.5 shrink-0"}),
            span("A trusted CA can sign certificates for any site. Only do this for a server you control, from a machine you trust, and remove the CA when you no longer need it.")),
    );
}

// caTrustHelp returns a binding that renders the help as a centred overlay
// while `open` is true. Closes on the backdrop, the close button, and Escape.
// The body is rebuilt on each open so the PEM fetch is fresh.
export function caTrustHelp(open) {
    const onKey = (e) => { if (e.key === "Escape") open.val = false; };
    return () => {
        if (!open.val) {
            window.removeEventListener("keydown", onKey);
            return "";
        }
        window.addEventListener("keydown", onKey);
        return div(
            {class: "fixed inset-0 z-40 flex items-center justify-center bg-black/60 p-4 backdrop-blur-[2px]",
                onclick: (e) => { if (e.target === e.currentTarget) open.val = false; }},
            div(
                {class: "flex max-h-[88vh] w-[600px] max-w-full flex-col overflow-hidden rounded-xl border border-gray-700/80 bg-surface shadow-2xl shadow-black/60",
                    role: "dialog", "aria-modal": "true", "aria-label": "Trust this server's certificate", "data-testid": "login-ca-help"},
                div(
                    {class: "flex items-start justify-between gap-4 border-b border-gray-700/80 px-5 py-4"},
                    div(
                        {class: "flex items-start gap-3"},
                        span({class: "flex h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-brand/15 text-brand ring-1 ring-brand/30"}, shieldCheckIcon({class: "h-[18px] w-[18px]"})),
                        div(
                            p({class: "text-base font-semibold text-white"}, "Trust this server's certificate"),
                            p({class: "mt-0.5 text-xs text-gray-400"}, "The server signs its own TLS certificate. Trust its CA once and the browser warning goes away."),
                        ),
                    ),
                    button({type: "button", class: "-mr-1 -mt-1 shrink-0 cursor-pointer rounded p-1 text-gray-400 transition-colors hover:bg-gray-700 hover:text-gray-100", title: "Close", "aria-label": "Close", onclick: () => { open.val = false; }},
                        closeIcon({class: "h-4 w-4"})),
                ),
                div({class: "app-scroll min-h-0 overflow-y-auto px-5 py-5"}, caTrustHelpBody()),
            ),
        );
    };
}
