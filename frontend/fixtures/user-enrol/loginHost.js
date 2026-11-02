// The widget reached from the login screen: the master password unlocks it,
// on the shared auth card, with a step strip above. This is what a
// src/pages/enrol.js would be once the endpoints exist.
import van from "vanjs-core";
import {spinnerButton} from "/src/components/spinnerbutton.js";
import {authButtonBase, authButtonClass, authCard, authLinkClass, fieldGroup, fieldInput, fieldRow, footerBand, footerText, notice, revealToggle, transportRow} from "/src/components/authCard.js";
import {userEnrolWidget} from "/src/components/userEnrol.js";
import {checkIcon} from "/src/lib/icons.js";

const {a, div, form, p, span} = van.tags;

const METHODS = {status: "ready", passkeyLoginEnabled: true, requirePasskey: false, localCaAvailable: false, error: ""};

export function loginHost({actions, data, onSignedIn, onBackToLogin}) {
    const step = van.state("password");
    const status = van.state("");
    const caOpen = van.state(false);

    // Built when the password step completes, not up front: VanJS drops the
    // bindings of DOM that sits disconnected for about a second.
    const widget = van.state(null);
    const buildWidget = () => userEnrolWidget({
        context: "login",
        actions,
        ...data,
        onSignedIn,
        onDone: onBackToLogin,
    });

    const stepStrip = () => {
        const current = step.val === "password" ? 0 : widget.val?.phase.val === "done" ? 2 : 1;
        const marker = (n, state) => span({
            class: `flex h-5 w-5 shrink-0 items-center justify-center rounded-full border text-[11px] font-medium ${
                state === "current" ? "border-brand/70 bg-brand/15 text-blue-100"
                    : state === "done" ? "border-emerald-700/70 bg-emerald-950/40 text-emerald-300"
                        : "border-gray-700 text-gray-500"}`,
        }, state === "done" ? checkIcon({class: "h-3 w-3"}) : String(n));
        const item = (n, text) => {
            const state = n - 1 < current ? "done" : n - 1 === current ? "current" : "next";
            return div({class: `flex items-center gap-2 text-xs ${state === "current" ? "text-gray-100" : "text-gray-500"}`}, marker(n, state), text);
        };
        const rule = () => div({class: "h-px flex-1 bg-gray-700/80"});
        return div({class: "flex items-center gap-3"}, item(1, "Master password"), rule(), item(2, "Enrol or recover"), rule(), item(3, "Done"));
    };

    const passwordStep = () => {
        const passwordInput = fieldInput({
            type: "password",
            "data-testid": "enrol-master-password-input",
            required: true,
            placeholder: "Master password",
            autocomplete: "off",
        });
        const submit = spinnerButton("Continue", null, authButtonClass, "submit", null, {base: authButtonBase});
        submit.dataset.testid = "enrol-master-continue";
        return form(
            {
                class: "flex flex-col gap-4",
                onsubmit: async (e) => {
                    e.preventDefault();
                    submit.isSubmitting.val = true;
                    status.val = "";
                    try {
                        await actions.verifyMasterPassword(passwordInput.value);
                        passwordInput.value = "";
                        widget.val = buildWidget();
                        step.val = "widget";
                    } catch (err) {
                        status.val = notice("error", "enrol-master-error", err?.message || "Authentication failed.");
                    } finally {
                        submit.isSubmitting.val = false;
                    }
                },
            },
            p({class: "text-sm text-gray-400"}, "The master password from the install lets you enrol a new user or recover an existing one."),
            fieldGroup(fieldRow("Master password", passwordInput, revealToggle(passwordInput))),
            submit,
        );
    };

    return authCard({
        title: () => step.val === "password" || !widget.val ? "Enrol or recover" : widget.val.title.val,
        width: () => step.val === "password" ? "max-w-[420px]" : "max-w-[620px]",
        body: [
            stepStrip,
            () => status.val || "",
            () => step.val === "password" || !widget.val ? passwordStep() : widget.val.node,
        ],
        footer: () => footerBand(
            transportRow(METHODS, caOpen, "enrol-ca-open"),
            footerText("Already have access? ", a({class: authLinkClass, "data-testid": "enrol-back-to-login", onclick: onBackToLogin}, "Back to login")),
        ),
    });
}
