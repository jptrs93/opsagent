import {usersMapS} from "../state/deployments.js";

export const resolveUserDisplayName = (userId) => {
    if (!userId) return null;
    return usersMapS.val.get(userId) || 'unknown';
};
