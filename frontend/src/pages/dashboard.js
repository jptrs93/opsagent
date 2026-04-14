import van from "vanjs-core";
import {loginS, clearLoginState} from "../state/login.js";
import {navigate} from "../lib/router.js";
import {sidebar} from "../components/sidebar.js";
import {statusPage} from "./status.js";
import {clusterPage} from "./cluster.js";

const { div, h1, span } = van.tags;

export function dashboard() {
    if (!loginS.val) {
        navigate("/login", {replace: true});
        return div();
    }

    const activePage = van.state('status');

    return div(
        {class: "h-dvh min-h-dvh w-dvw flex overflow-hidden"},
        sidebar(activePage),
        div(
            {class: "flex-1 min-h-0 overflow-hidden"},
            () => {
                if (activePage.val === 'status') return statusPage();
                if (activePage.val === 'cluster') return clusterPage();
                return div({class: "p-6"}, "Unknown page");
            }
        )
    );
}
