import van from "vanjs-core";
import {loginPage} from "./pages/login.js";
import {enrolPage} from "./pages/enrol.js";
import {enrolLandingPage} from "./pages/enrolLanding.js";
import {dashboard} from "./pages/dashboard.js";
import "./state/deployments.js";
import {clearLoginState, initLoginState, loginS, setLoginFromResponse} from "./state/login.js";
import {currentPath, navigate} from "./lib/router.js";
import {isRootSession} from "./lib/userSessions.js";
import {capi} from "./capi/index.js";

function renderRoute() {
    const path = currentPath.val;
    const landing = /^\/enrol\/([^/]+)$/.exec(path);
    if (landing) return enrolLandingPage(decodeURIComponent(landing[1]));
    if (path === "/enrol" || path === "/bootstrap") return enrolPage();
    // A root session is not a user: it stays on the enrol page.
    if (path === "/" && isRootSession(loginS.val)) return enrolPage();
    if (path === "/") return dashboard();
    return loginPage();
}

if (!window.__opsagentAppInited) {
    window.__opsagentAppInited = true;
    initLoginState().then(async () => {
        if (loginS.val) {
            try {
                const response = await capi.getV1AuthCurrentSession();
                setLoginFromResponse(response);
            } catch (e) {
                console.log(`error validating session, clearing: ${e}`);
                clearLoginState();
            }
        }
        if (window.location.pathname === "/" && !loginS.val) {
            navigate("/login", {replace: true});
        }
        van.add(document.body, renderRoute);
    });
}
