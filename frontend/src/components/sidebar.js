import van from "vanjs-core";
import {clearLoginState} from "../state/login.js";
import {deploymentsStreamS} from "../state/deployments.js";
import {navigate} from "../lib/router.js";

const { div, span, h2, p } = van.tags;

const streamStatusClass = (status) => status === 'connected' ? 'text-green-400' : 'text-red-400';

const streamDotClass = (status) => status === 'connected'
    ? 'bg-green-400 animate-pulse [animation-duration:2s]'
    : 'bg-red-400';

export function sidebar(activePage) {
    const item = (label, key) => {
        return div({
            class: () => `px-4 py-2 rounded cursor-pointer text-sm transition-colors ${
                activePage.val === key
                    ? 'bg-surface text-white'
                    : 'text-gray-400 hover:text-gray-200 hover:bg-surface-hover'
            }`,
            onclick: () => activePage.val = key
        }, label);
    };

    return div(
        {class: "w-56 bg-sidebar border-r border-gray-800 flex flex-col min-h-dvh"},
        div(
            {class: "p-4 border-b border-gray-800"},
            h2({class: "text-lg font-bold text-white"}, "OpsAgent"),
            div(
                {class: "mt-1 flex items-center gap-2"},
                span({
                    class: () => `h-2 w-2 rounded-full ${streamDotClass(deploymentsStreamS.val.status)}`,
                }),
                p({
                    class: () => `text-xs ${streamStatusClass(deploymentsStreamS.val.status)}`,
                }, () => deploymentsStreamS.val.sentence)
            )
        ),
        div(
            {class: "p-3 flex flex-col gap-1"},
            div({class: "text-xs font-semibold text-gray-500 uppercase tracking-wider px-4 py-2"}, "Deployments"),
            item("Status", "status"),
            item("Config", "config"),
        ),
        div({class: "flex-1"}),
        div(
            {class: "p-3 border-t border-gray-800"},
            item("Cluster", "cluster"),
        ),
        div(
            {class: "p-3 border-t border-gray-800"},
            div({
                class: "px-4 py-2 text-sm text-gray-400 hover:text-gray-200 cursor-pointer rounded hover:bg-surface-hover transition-colors",
                onclick: () => {
                    clearLoginState();
                    navigate("/login");
                }
            }, "Sign out")
        )
    );
}
