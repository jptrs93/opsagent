import van from "vanjs-core";
import {loginS} from "../state/login.js";
import {
    assetMetasS,
    secretMetasS,
    userConfigsS,
    usersMapS,
} from "../state/deployments.js";

const { div, p, span, input, table, thead, tbody, tr, th, td, colgroup, col } = van.tags;

// Users are read-only from the browser: they come into existence through
// passkey registration during bootstrap or recovery, and the state stream
// carries only id + name. Everything else on this page is derived from
// created-by attribution on the items the user has made.
const sortedUsers = () => [...usersMapS.val.entries()]
    .map(([id, name]) => ({id: Number(id), name: name || ""}))
    .sort((a, b) => a.id - b.id);

// Counting distinct live items (not versions) matches what the operator means
// by "how many secrets has this user created" — same semantics as the space
// counts on the spaces page.
const countCreatedBy = (items, userID) => {
    let count = 0;
    for (const item of items || []) {
        if (!item || item.deleted) continue;
        if (Number(item.createdBy || 0) === userID) count++;
    }
    return count;
};

export function usersPage() {
    const search = van.state("");

    const filteredUsers = () => {
        const query = search.val.trim().toLowerCase();
        const users = sortedUsers();
        if (!query) return users;
        return users.filter((user) => user.name.toLowerCase().includes(query));
    };

    const countCell = (value) => td(
        {class: "py-1 pr-3 text-gray-400 whitespace-nowrap tabular-nums"},
        String(value),
    );

    const userRow = (user) => {
        const isSelf = Number(loginS.val?.userId || 0) === user.id;
        return tr(
            {class: "border-b border-gray-800 last:border-0 align-middle", "data-testid": `user-row-${user.id}`},
            td({class: "py-1 pr-3 min-w-0"},
                div({class: "flex items-center gap-2 px-2 py-1 min-w-0"},
                    span({class: "truncate text-gray-200"}, user.name || `user ${user.id}`),
                    isSelf ? span(
                        {class: "shrink-0 rounded bg-gray-800 px-1.5 py-0.5 text-[10px] uppercase tracking-wide text-gray-500"},
                        "you") : "",
                )),
            countCell(user.id),
            countCell(countCreatedBy(secretMetasS.val, user.id)),
            countCell(countCreatedBy(userConfigsS.val, user.id)),
            countCell(countCreatedBy(assetMetasS.val, user.id)),
        );
    };

    const listPanel = () => div(
        {class: "card flex-1 flex flex-col gap-3 min-w-0 min-h-0"},
        div({class: "flex flex-wrap items-center justify-between gap-3"},
            input({
                class: "text-input search-input",
                type: "search",
                placeholder: "Search users",
                value: search,
                oninput: (e) => search.val = e.target.value,
            })),
        div({class: "deployment-table-scroll flex-1 min-h-0 overflow-auto"}, () => {
            const visible = filteredUsers();
            if (!visible.length) {
                return p({class: "text-gray-400 text-sm"},
                    search.val.trim() ? "No users match your search." : "No users yet.");
            }
            return table(
                {class: "w-full table-fixed text-sm"},
                colgroup(
                    col({style: "width:40%"}),
                    col({style: "width:12%"}),
                    col({style: "width:16%"}),
                    col({style: "width:16%"}),
                    col({style: "width:16%"}),
                ),
                thead(tr({class: "text-left text-gray-400 border-b border-gray-700"},
                    th({class: "pb-2 pr-3 font-medium"}, "Name"),
                    th({class: "pb-2 pr-3 font-medium"}, "ID"),
                    th({class: "pb-2 pr-3 font-medium"}, "Secrets created"),
                    th({class: "pb-2 pr-3 font-medium"}, "Configs created"),
                    th({class: "pb-2 pr-3 font-medium"}, "Assets created"))),
                tbody(...visible.map(userRow)),
            );
        }),
    );

    return div(
        {class: "h-full min-h-0 overflow-hidden p-3 flex flex-col gap-3"},
        div({class: "flex-1 flex flex-col min-h-0"}, listPanel),
    );
}
