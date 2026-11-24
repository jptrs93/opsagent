import van from "vanjs-core";
import {loginS, clearLoginState} from "../state/login.js";
import {navigate} from "../lib/router.js";
import {sidebar} from "../components/sidebar.js";
import {deploymentsPage} from "./deployments.js";
import {clusterPage} from "./cluster.js";
import {secretsPage} from "./secrets.js";
import {assetsPage, preloadAssetCodeEditor} from "./assets.js";
import {spacesPage} from "./spaces.js";
import {policiesPage} from "./policies.js";
import {plannedPage, PLANNED} from "./planned.js";
import {usersPage} from "./users.js";
import {settingsPage} from "./settings.js";
import {sessionsPage} from "./sessions.js";
import {logsPage} from "./logs.js";
import {metricsPage} from "./metrics.js";
import {preloadDeploymentCodeWidget} from "../components/deploymentEditorWidget.js";
import {userEnrolOverlay} from "../components/userEnrol.js";
import {enrolActions, enrolData} from "../state/enrol.js";
import {accountActions, accountSelf, loadAccount} from "../state/account.js";
import {isRootSession} from "../lib/userSessions.js";

const { div, h1, span } = van.tags;

export function dashboard() {
    if (!loginS.val) {
        navigate("/login", {replace: true});
        return div();
    }
    if (isRootSession(loginS.val)) {
        navigate("/enrol", {replace: true});
        return div();
    }

    const activePage = van.state('status');
    const selectedLogDeploymentId = van.state(0);

    const openLogsForDeployment = (deploymentId) => {
        selectedLogDeploymentId.val = deploymentId;
        activePage.val = 'logs';
    };

    requestAnimationFrame(() => {
        preloadAssetCodeEditor();
        preloadDeploymentCodeWidget();
    });

    // The deployments page stays mounted while other pages show, so its open
    // editor tabs and drafts survive a detour through Logs or Settings. It is
    // built here, outside the page binding below: VanJS ties every derive
    // created inside a reactive child to that child's DOM and drops them once
    // the child is replaced, which would freeze the table after a detour.
    const deploymentsHost = div(
        {class: () => activePage.val === 'status' ? 'h-full' : 'hidden'},
        deploymentsPage(openLogsForDeployment),
    );
    // The settings page is kept the same way: it reads its draft while building
    // its rows, so inside the binding every edit would rebuild the page.
    const settingsHost = div(
        {class: () => activePage.val === 'settings' ? 'h-full' : 'hidden'},
        settingsPage(),
    );
    const metricsHost = div(
        {class: () => activePage.val === 'metrics' ? 'h-full' : 'hidden'},
        metricsPage({active: () => activePage.val === 'metrics'}),
    );

    return div(
        {class: "h-dvh min-h-dvh w-dvw flex overflow-hidden"},
        sidebar(activePage),
        div(
            {class: "h-full flex-1 min-w-0 min-h-0 overflow-hidden"},
            deploymentsHost,
            settingsHost,
            metricsHost,
            () => {
                if (activePage.val === 'status') return '';
                if (activePage.val === 'logs') return logsPage(selectedLogDeploymentId);
                if (activePage.val === 'metrics') return '';
                if (activePage.val === 'secrets') return secretsPage();
                if (activePage.val === 'configs') return secretsPage();
                if (activePage.val === 'assets') return assetsPage();
                if (activePage.val === 'spaces') return spacesPage();
                if (activePage.val === 'policies') return policiesPage();
                if (PLANNED.has(activePage.val)) return plannedPage();
                if (activePage.val === 'users') {
                    loadAccount().catch((e) => console.log(`error loading account: ${e}`));
                    return usersPage({
                        enrol: ({onClose}) => userEnrolOverlay({onClose, actions: enrolActions, ...enrolData}),
                        account: {self: accountSelf, actions: accountActions, onManageSessions: () => { activePage.val = 'sessions'; }},
                    });
                }
                if (activePage.val === 'sessions') return sessionsPage();
                if (activePage.val === 'cluster') return clusterPage();
                if (activePage.val === 'settings') return '';
                return div({class: "p-3"}, "Unknown page");
            }
        )
    );
}
