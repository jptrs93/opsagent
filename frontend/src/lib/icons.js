import van from "vanjs-core";

const {svg, path, circle, line, polyline} = van.tags("http://www.w3.org/2000/svg");

const iconAttrs = ({size, class: className, ...attrs} = {}) => ({
    viewBox: "0 0 24 24",
    fill: "none",
    stroke: "currentColor",
    "stroke-width": "2",
    "stroke-linecap": "round",
    "stroke-linejoin": "round",
    ...(size ? {width: size, height: size} : {class: className || "w-4 h-4"}),
    ...(size && className ? {class: className} : {}),
    ...attrs,
});

export const closeIcon = (attrs) => svg(iconAttrs(attrs),
    line({x1: "18", y1: "6", x2: "6", y2: "18"}),
    line({x1: "6", y1: "6", x2: "18", y2: "18"}),
);

export const copyIcon = (attrs) => svg(iconAttrs(attrs),
    path({d: "M8 8h11a1 1 0 0 1 1 1v11a1 1 0 0 1-1 1H8a1 1 0 0 1-1-1V9a1 1 0 0 1 1-1Z"}),
    path({d: "M4 16c-.55 0-1-.45-1-1V4c0-.55.45-1 1-1h11c.55 0 1 .45 1 1"}),
);

export const checkIcon = (attrs) => svg(iconAttrs(attrs),
    polyline({points: "20 6 9 17 4 12"}),
);

export const eyeOpenIcon = (attrs) => svg(iconAttrs(attrs),
    path({d: "M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7-10-7-10-7Z"}),
    circle({cx: "12", cy: "12", r: "3"}),
);

export const eyeOffIcon = (attrs) => svg(iconAttrs(attrs),
    path({d: "M9.9 4.24A9.1 9.1 0 0 1 12 4c7 0 10 8 10 8a18.5 18.5 0 0 1-2.16 3.19m-6.72-1.07a3 3 0 1 1-4.24-4.24"}),
    path({d: "M6.61 6.61A18.5 18.5 0 0 0 2 12s3 8 10 8a9.1 9.1 0 0 0 5.39-1.61"}),
    line({x1: "2", y1: "2", x2: "22", y2: "22"}),
);

export const editIcon = (attrs) => svg(iconAttrs(attrs),
    path({d: "M12 20h9"}),
    path({d: "M16.5 3.5a2.12 2.12 0 0 1 3 3L7 19l-4 1 1-4Z"}),
);

export const expandIcon = (attrs) => svg(iconAttrs(attrs),
    polyline({points: "15 3 21 3 21 9"}),
    polyline({points: "9 21 3 21 3 15"}),
    line({x1: "21", y1: "3", x2: "14", y2: "10"}),
    line({x1: "3", y1: "21", x2: "10", y2: "14"}),
);

export const infoIcon = (attrs) => svg(iconAttrs(attrs),
    circle({cx: "12", cy: "12", r: "10"}),
    line({x1: "12", y1: "16", x2: "12", y2: "12"}),
    line({x1: "12", y1: "8", x2: "12.01", y2: "8"}),
);

export const plusIcon = (attrs) => svg(iconAttrs(attrs),
    line({x1: "12", y1: "5", x2: "12", y2: "19"}),
    line({x1: "5", y1: "12", x2: "19", y2: "12"}),
);

export const refreshIcon = (attrs) => svg(iconAttrs(attrs),
    polyline({points: "23 4 23 10 17 10"}),
    polyline({points: "1 20 1 14 7 14"}),
    path({d: "M3.51 9a9 9 0 0 1 14.85-3.36L23 10"}),
    path({d: "M20.49 15a9 9 0 0 1-14.85 3.36L1 14"}),
);

export const trashIcon = (attrs) => svg(iconAttrs(attrs),
    path({d: "M3 6h18"}),
    path({d: "M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6"}),
    path({d: "M10 11v6M14 11v6"}),
    path({d: "M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"}),
);

export const xIcon = closeIcon;

export const caretRightIcon = (attrs) => svg(iconAttrs(attrs),
    polyline({points: "9 6 15 12 9 18"}),
);

export const chevronDownIcon = (attrs) => svg(iconAttrs(attrs),
    polyline({points: "6 9 12 15 18 9"}),
);

export const sortArrowIcon = (attrs) => svg(iconAttrs(attrs),
    polyline({points: "6 15 12 9 18 15"}),
);

export const folderIcon = (attrs) => svg(iconAttrs(attrs),
    path({d: "M3 7a1 1 0 0 1 1-1h5l2 2h8a1 1 0 0 1 1 1v9a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V7Z"}),
);

export const secretKeyIcon = (attrs) => svg(iconAttrs(attrs),
    circle({cx: "8", cy: "14", r: "4"}),
    path({d: "M11 11 20 2"}),
    path({d: "m17 5 2 2"}),
    path({d: "m14 8 2 2"}),
);

export const configSlidersIcon = (attrs) => svg(iconAttrs(attrs),
    path({d: "M5 6h14"}),
    path({d: "M5 12h14"}),
    path({d: "M5 18h14"}),
    circle({cx: "9", cy: "6", r: "1.6", fill: "currentColor"}),
    circle({cx: "15", cy: "12", r: "1.6", fill: "currentColor"}),
    circle({cx: "10", cy: "18", r: "1.6", fill: "currentColor"}),
);

export const columnsIcon = (attrs) => svg(iconAttrs(attrs),
    path({d: "M3 5a1 1 0 0 1 1-1h16a1 1 0 0 1 1 1v14a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V5Z"}),
    path({d: "M9 4v16"}),
    path({d: "M15 4v16"}),
);

export const searchIcon = (attrs) => svg(iconAttrs(attrs),
    circle({cx: "11", cy: "11", r: "7"}),
    path({d: "m20 20-3.5-3.5"}),
);

export const fileIcon = (attrs) => svg(iconAttrs(attrs),
    path({d: "M6 3a1 1 0 0 0-1 1v16a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V8l-5-5H6Z"}),
    path({d: "M13 3v6h6"}),
);

export const uploadIcon = (attrs) => svg(iconAttrs(attrs),
    path({d: "M12 15V4"}),
    polyline({points: "7 9 12 4 17 9"}),
    path({d: "M4 16v3a1 1 0 0 0 1 1h14a1 1 0 0 0 1-1v-3"}),
);
