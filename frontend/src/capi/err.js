import {decodeApiErr} from "./model.js";
import {navigate} from "../lib/router.js";
import {clearLoginState, loginS} from "../state/login.js";


export async function handleErr(response) {
    if (!response.ok) {
        let msg = `Unknown server error: ${response.status}`
        let apiErr = null;
        try {
            apiErr = decodeApiErr(await response.arrayBuffer())
            if(apiErr.displayErr.length > 0) {
                msg = apiErr.displayErr
            }
        } catch (e) {}
        if (response.status === 401 && loginS.val) {
            clearLoginState();
            navigate("/login", {replace: true});
            if (msg === `Unknown server error: ${response.status}`) {
                msg = 'Session expired. Please sign in again.';
            }
        }
        const err = new Error(msg);
        err.status = response.status;
        err.apiErr = apiErr;
        throw err;
    }
}
