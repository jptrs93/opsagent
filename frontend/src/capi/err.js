import {decodeApiErr} from "./model.js";
import {navigate} from "../lib/router.js";
import {clearLoginState, loginS} from "../state/login.js";


export async function handleErr(response) {
    if (!response.ok) {
        let msg = `Unknown server error: ${response.status}`
        try {
            const serverErr = decodeApiErr(await response.arrayBuffer())
            if(serverErr.displayErr.length > 0) {
                msg = serverErr.displayErr
            }
        } catch (e) {}
        if (response.status === 401 && loginS.val) {
            clearLoginState();
            navigate("/login", {replace: true});
            if (msg === `Unknown server error: ${response.status}`) {
                msg = 'Session expired. Please sign in again.';
            }
        }
        throw new Error(msg);
    }
}
