import {STATUS} from "/src/lib/agentSessions.js";

// All timestamps are built relative to load time so "Active until" and
// "Expired" read plausibly whenever the fixture is opened.
const now = Date.now();
const min = 60 * 1000;
const hour = 60 * min;
const day = 24 * hour;

const at = (offsetMs) => new Date(now + offsetMs);
const none = new Date(0);

let nextID = 1;
const agent = (spec) => ({
    id: `as-${nextID++}`,
    approvalCode: "",
    requestingAddress: "203.0.113.7",
    expiresAt: none,
    ...spec,
});

const personal = (spec) => ({
    id: `ps-${nextID++}`,
    current: false,
    revoked: false,
    ...spec,
});

// Each scenario is a factory so approving / revoking in the fixture never
// bleeds into the next scenario selection.
export const scenarios = {
    typical: () => ({
        agentSessions: [
            agent({status: STATUS.PENDING, createdAt: at(-2 * min), approvalCode: "XKCD-2932", requestingAddress: "198.51.100.23"}),
            agent({status: STATUS.APPROVED, createdAt: at(-3 * hour), expiresAt: at(9 * hour), requestingAddress: "198.51.100.23"}),
            agent({status: STATUS.APPROVED, createdAt: at(-26 * hour), expiresAt: none, requestingAddress: "203.0.113.7"}),
            agent({status: STATUS.APPROVED, createdAt: at(-3 * day), expiresAt: at(-2 * day), requestingAddress: "203.0.113.7"}),
            agent({status: STATUS.REVOKED, createdAt: at(-5 * day), requestingAddress: "192.0.2.44"}),
            agent({status: STATUS.REJECTED, createdAt: at(-6 * day), requestingAddress: "192.0.2.44"}),
        ],
        personalSessions: [
            personal({createdAt: at(-2 * hour), lastActiveAt: at(-2 * min), expiresAt: at(22 * hour), device: "Chrome 126 · macOS", ip: "82.14.30.9", current: true}),
            personal({createdAt: at(-2 * day), lastActiveAt: at(-5 * hour), expiresAt: at(20 * hour), device: "Safari · iPhone", ip: "82.14.30.9"}),
            personal({createdAt: at(-9 * day), lastActiveAt: at(-8 * day), expiresAt: at(-8 * day), device: "Firefox 127 · Windows", ip: "146.70.99.2"}),
            personal({createdAt: at(-12 * day), lastActiveAt: at(-11 * day), expiresAt: at(-11 * day), device: "Chrome 125 · Linux", ip: "10.40.0.6", revoked: true}),
        ],
    }),
    many: () => ({
        agentSessions: [
            agent({status: STATUS.PENDING, createdAt: at(-1 * min), approvalCode: "BLUE-8471", requestingAddress: "198.51.100.23"}),
            agent({status: STATUS.PENDING, createdAt: at(-4 * min), approvalCode: "MOSS-1108", requestingAddress: "198.51.100.24"}),
            ...Array.from({length: 9}, (_, i) => agent({
                status: STATUS.APPROVED,
                createdAt: at(-(i + 1) * 5 * hour),
                expiresAt: at((12 - i * 3) * hour),
                requestingAddress: i % 2 ? "203.0.113.7" : "198.51.100.23",
            })),
            ...Array.from({length: 8}, (_, i) => agent({
                status: i % 3 === 0 ? STATUS.REVOKED : STATUS.REJECTED,
                createdAt: at(-(i + 4) * day),
                requestingAddress: "192.0.2.44",
            })),
        ],
        personalSessions: [
            personal({createdAt: at(-1 * hour), lastActiveAt: at(-1 * min), expiresAt: at(23 * hour), device: "Chrome 126 · macOS", ip: "82.14.30.9", current: true}),
            ...Array.from({length: 9}, (_, i) => personal({
                createdAt: at(-(i + 1) * day),
                lastActiveAt: at(-(i + 1) * day + 6 * hour),
                expiresAt: at(-(i) * day - 2 * hour),
                device: ["Safari · iPhone", "Firefox 127 · Windows", "Chrome 125 · Linux", "Edge 126 · Windows"][i % 4],
                ip: ["82.14.30.9", "146.70.99.2", "10.40.0.6"][i % 3],
                revoked: i % 4 === 3,
            })),
        ],
    }),
    empty: () => ({agentSessions: [], personalSessions: []}),
};
