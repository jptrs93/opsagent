import van from "vanjs-core";
import {loginPage} from "./pages/login.js";
import {bootstrapPage} from "./pages/bootstrap.js";
import {dashboard} from "./pages/dashboard.js";
import "./state/deployments.js";
import {initLoginState, loginS, setLoginFromResponse} from "./state/login.js";
import {currentPath, navigate} from "./lib/router.js";
import {capi} from "./capi/index.js";

function renderRoute() {
    const path = currentPath.val;
    if (path === "/bootstrap") return bootstrapPage();
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
                console.log(`error loading current session: ${e}`);
            }
        }
        if (window.location.pathname === "/" && !loginS.val) {
            navigate("/login", {replace: true});
        }
        van.add(document.body, renderRoute);
    });
}
