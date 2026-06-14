const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

function pad2(n) {
    return String(n).padStart(2, '0');
}

export function formatDateTime(value, fallback = '') {
    if (!(value instanceof Date) || value.getTime() <= 0) return fallback;
    return `${MONTHS[value.getMonth()]} ${value.getDate()}, ${pad2(value.getHours())}:${pad2(value.getMinutes())}`;
}

export function formatHistoryTime(value) {
    if (!(value instanceof Date) || value.getTime() <= 0) return '';
    return `${MONTHS[value.getMonth()]} ${value.getDate()} ${pad2(value.getHours())}:${pad2(value.getMinutes())}:${pad2(value.getSeconds())}`;
}

export function formatClockTime(value) {
    if (!(value instanceof Date) || value.getTime() <= 0) return '';
    return `${pad2(value.getHours())}:${pad2(value.getMinutes())}:${pad2(value.getSeconds())}`;
}
