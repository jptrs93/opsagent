import van from "vanjs-core";
import {loginS, clearLoginState} from "../state/login.js";
import {navigate} from "../lib/router.js";
import {sidebar} from "../components/sidebar.js";
import {statusPage} from "./status.js";
import {clusterPage} from "./cluster.js";
import {secretsPage} from "./secrets.js";
import {configsPage} from "./configs.js";
import {assetsPage} from "./assets.js";
import {settingsPage} from "./settings.js";
import {logsPage} from "./logs.js";

const { div, h1, span } = van.tags;

export function dashboard() {
    if (!loginS.val) {
        navigate("/login", {replace: true});
        return div();
    }

    const activePage = van.state('status');
    const selectedLogDeploymentId = van.state(0);

    const openLogsForDeployment = (deploymentId) => {
        selectedLogDeploymentId.val = deploymentId;
        activePage.val = 'logs';
    };

    return div(
        {class: "h-dvh min-h-dvh w-dvw flex overflow-hidden"},
        sidebar(activePage),
        div(
            {class: "flex-1 min-h-0 overflow-hidden"},
            () => {
                if (activePage.val === 'status') return statusPage(openLogsForDeployment);
                if (activePage.val === 'logs') return logsPage(selectedLogDeploymentId);
                if (activePage.val === 'secrets') return secretsPage();
                if (activePage.val === 'configs') return configsPage();
                if (activePage.val === 'assets') return assetsPage();
                if (activePage.val === 'cluster') return clusterPage();
                if (activePage.val === 'settings') return settingsPage();
                return div({class: "p-6"}, "Unknown page");
            }
        )
    );
}
