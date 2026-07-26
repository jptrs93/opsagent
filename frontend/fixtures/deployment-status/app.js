import van from "vanjs-core";
import {statusRow} from "/src/components/statusCard.js";

const {div, h1, p, span, table, thead, tbody, tr, th, td, colgroup, col} = van.tags;

const columns = () => colgroup(
    col({style: "width:10rem"}),
    col({style: "width:8rem"}),
    col({style: "width:7rem"}),
    col({style: "width:8rem"}),
    col({style: "width:9rem"}),
    col({style: "width:7rem"}),
    col({style: "width:7rem"}),
    col({style: "width:8rem"}),
    col({style: "width:11.5rem"}),
);

const tableClass = "w-full min-w-[75rem] table-fixed text-left text-sm";
const header = table(
    {class: tableClass},
    columns(),
    thead(
        tr(
            {class: "border-b border-gray-700 text-xs uppercase tracking-wide text-gray-500"},
            th({class: "py-3 pl-4 pr-3 font-medium"}, "Deployment"),
            th({class: "py-3 px-3 font-medium"}, "Node"),
            th({class: "py-3 px-3 font-medium"}, "Status"),
            th({class: "py-3 px-3 font-medium"}, "Version"),
            th({class: "py-3 px-3 font-medium"}, "Prepare"),
            th({class: "py-3 px-3 font-medium"}, "Restarts"),
            th({class: "py-3 px-3 font-medium"}, "Deployed by"),
            th({class: "py-3 px-3 font-medium"}, "Deployed at"),
            th({class: "py-3 pl-3 pr-1 font-medium text-right"}, "Actions"),
        ),
    ),
);

const rows = Array.from({length: 28}, (_, index) => {
    const row = {
        id: index + 1,
        name: ["api", "worker", "web", "scheduler"][index % 4] + `-${index + 1}`,
        spaceName: index % 3 === 0 ? "production" : "default",
        node: index % 2 === 0 ? "primary" : "worker-a",
        existingStatus: index % 5 === 0 ? 3 : 2,
        runnerPresent: true,
        existingVersion: "v0.0.195",
        deployedVersion: "v0.0.195",
        prepareStatus: 4,
        prepareVersion: "v0.0.195",
        numberOfRestarts: index % 4,
        lastRestartAt: new Date("2026-07-20T12:00:00Z"),
        deployedBy: 0,
        deployedAt: new Date("2026-07-20T12:00:00Z"),
        runnerType: "container",
        canDelete: true,
    };
    if (index === 0) {
        row.scheduledInstances = [
            {
                instanceId: 100,
                existingStatus: 2,
                runnerPresent: true,
                existingVersion: "v0.0.194",
                deployedVersion: "v0.0.195",
                prepareStatus: 4,
            },
            {
                instanceId: 101,
                existingStatus: 0,
                runnerPresent: false,
                existingVersion: "v0.0.195",
                deployedVersion: "v0.0.195",
                prepareStatus: 2,
            },
        ];
    }
    return row;
});

const spaceDivider = (space, first) => tr(
    td(
        {colSpan: 9, class: `${first ? "pt-2 pb-3" : "py-3"} px-0`},
        div(
            {class: "flex items-center gap-3"},
            span({class: "text-xs font-semibold tracking-wide text-blue-300 whitespace-nowrap"}, space),
            div({class: "h-px flex-1 bg-gradient-to-r from-gray-600/80 to-transparent"}),
        ),
    ),
);

const body = table(
    {class: tableClass},
    columns(),
    tbody(
        ...["production", "default"].flatMap((space, index) => [
            spaceDivider(space, index === 0),
            ...rows.filter(row => row.spaceName === space).map(deployment => statusRow(
                deployment,
                () => {},
                () => {},
                () => {},
                () => {},
                () => {},
                {showSpace: false},
            )),
        ]),
    ),
);

van.add(document.body,
    div(
        {class: "h-full min-h-0 overflow-hidden p-6 flex flex-col gap-4"},
        div(
            h1({class: "text-lg font-semibold text-white"}, "Deployment status table fixture"),
            p({class: "mt-1 text-sm text-gray-400"}, "Grouped mock rows include a rollover and force the production table body scrollbar for alignment checks."),
        ),
        div(
            {class: "w-full min-h-0 flex-1 rounded-lg bg-surface border border-gray-700 p-2 flex flex-col"},
            div(
                {class: "app-scroll-x w-full min-h-0 flex-1 overflow-x-auto overflow-y-hidden"},
                div(
                    {class: "h-full min-h-0 flex flex-col"},
                    div({class: "flex-none pr-1"}, header),
                    div({class: "deployment-table-scroll min-h-0 flex-1 overflow-y-auto overflow-x-hidden pr-1"}, body),
                ),
            ),
        ),
    ),
);
