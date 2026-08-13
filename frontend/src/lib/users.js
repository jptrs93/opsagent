import {usersMapS} from "../state/deployments.js";

// Negative ids mean an agent acting on the user's behalf: -id is the user id.
export const resolveUserDisplayName = (userId) => {
    if (!userId) return null;
    if (userId < 0) {
        const name = usersMapS.val.get(-userId)?.name;
        if (!name) return 'unknown agent';
        return name.endsWith('s') ? `${name}' agent` : `${name}'s agent`;
    }
    return usersMapS.val.get(userId)?.name || 'unknown';
};
