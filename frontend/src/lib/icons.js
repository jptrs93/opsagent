import van from "vanjs-core";

const {svg, path, circle, line, polyline, rect} = van.tags("http://www.w3.org/2000/svg");

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

export const lockIcon = (attrs) => svg(iconAttrs(attrs),
    path({d: "M5 11h14a1 1 0 0 1 1 1v8a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1v-8a1 1 0 0 1 1-1Z"}),
    path({d: "M8 11V7a4 4 0 0 1 8 0v4"}),
);

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

export const logOutIcon = (attrs) => svg(iconAttrs(attrs),
    path({d: "M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"}),
    polyline({points: "16 17 21 12 16 7"}),
    line({x1: "21", y1: "12", x2: "9", y2: "12"}),
);

export const fingerprintIcon = (attrs) => svg(iconAttrs(attrs),
    path({d: "M12 10a2 2 0 0 0-2 2c0 1.02-.1 2.51-.26 4"}),
    path({d: "M14 13.12c0 2.38 0 6.38-1 8.88"}),
    path({d: "M17.29 21.02c.12-.6.43-2.3.5-3.02"}),
    path({d: "M2 12a10 10 0 0 1 18-6"}),
    path({d: "M2 16h.01"}),
    path({d: "M21.8 16c.2-2 .131-5.354 0-6"}),
    path({d: "M5 19.5C5.5 18 6 15 6 12a6 6 0 0 1 .34-2"}),
    path({d: "M8.65 22c.21-.66.45-1.32.57-2"}),
    path({d: "M9 6.8a6 6 0 0 1 9 5.2v2"}),
);

export const keyRoundIcon = (attrs) => svg(iconAttrs(attrs),
    path({d: "M2.586 17.414A2 2 0 0 0 2 18.828V21a1 1 0 0 0 1 1h3a1 1 0 0 0 1-1v-1a1 1 0 0 1 1-1h1a1 1 0 0 0 1-1v-1a1 1 0 0 1 1-1h.172a2 2 0 0 0 1.414-.586l.814-.814a6.5 6.5 0 1 0-4-4z"}),
    circle({cx: "16.5", cy: "7.5", r: ".5", fill: "currentColor"}),
);

export const shieldCheckIcon = (attrs) => svg(iconAttrs(attrs),
    path({d: "M20 13c0 5-3.5 7.5-7.66 8.95a1 1 0 0 1-.67-.01C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.24-2.72a1.17 1.17 0 0 1 1.52 0C14.51 3.81 17 5 19 5a1 1 0 0 1 1 1z"}),
    path({d: "m9 12 2 2 4-4"}),
);

export const alertCircleIcon = (attrs) => svg(iconAttrs(attrs),
    circle({cx: "12", cy: "12", r: "10"}),
    path({d: "M12 8v4"}),
    path({d: "M12 16h.01"}),
);

export const downloadIcon = (attrs) => svg(iconAttrs(attrs),
    path({d: "M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"}),
    path({d: "M7 10l5 5 5-5"}),
    path({d: "M12 15V3"}),
);

// logoMark is the product mark: a brand tile with a deploy glyph. Sized by
// `size` in px; decorative, so hidden from assistive tech.
export const logoMark = ({size = 28, class: className = ""} = {}) => svg(
    {viewBox: "0 0 32 32", width: size, height: size, class: className, fill: "none", "aria-hidden": "true"},
    rect({x: "1.5", y: "1.5", width: "29", height: "29", rx: "7.5", fill: "var(--color-brand)"}),
    path({d: "M16 8.5l7 11.5H9z", fill: "#fff", opacity: "0.95"}),
    path({d: "M11.5 23.5h9", stroke: "#fff", "stroke-width": "2", "stroke-linecap": "round", opacity: "0.55"}),
);
